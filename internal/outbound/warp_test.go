package outbound

import (
	"context"
	"errors"
	"net"
	"reflect"
	"rillway/internal/config"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWARPSplitCacheAndClockProgression(t *testing.T) {
	fakeNow := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	getNow := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return fakeNow
	}
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		fakeNow = fakeNow.Add(d)
	}

	w := newWARP(config.Outbound{ID: "warp", ProxyAddress: "127.0.0.1:40000"})
	w.now = getNow

	// Fake dialer so loopback probe succeeds without actual listener
	w.dialContext = func(_ context.Context, _, _ string) (net.Conn, error) {
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, nil
	}

	var recordedCommands []string
	var cmdMu sync.Mutex
	w.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		cmdMu.Lock()
		defer cmdMu.Unlock()
		cmd := strings.Join(args[1:], " ")
		recordedCommands = append(recordedCommands, cmd)
		switch cmd {
		case "--version":
			return []byte("WARP 2026.9.1.0"), nil
		case "settings":
			return []byte("Mode: WarpProxy on port 40000"), nil
		case "registration show":
			return []byte("Account type: Unlimited\nDevice ID: dev123"), nil
		case "status":
			return []byte("Status update: Connected"), nil
		default:
			return nil, errors.New("unexpected command: " + cmd)
		}
	}

	// 1. Cold start: Status must execute 4 CLI calls (3 metadata + 1 dynamic)
	s := w.Status(context.Background())
	if s.Version != "2026.9.1.0" || s.Mode != "proxy" || s.Account != "Unlimited" || s.State != "connected" || !s.Listener {
		t.Fatalf("unexpected status on cold start: %+v", s)
	}
	cmdMu.Lock()
	if len(recordedCommands) != 4 {
		t.Fatalf("expected 4 CLI calls on cold start, got %d: %v", len(recordedCommands), recordedCommands)
	}
	expectedCold := []string{"status", "--version", "settings", "registration show"}
	if !reflect.DeepEqual(recordedCommands, expectedCold) {
		t.Fatalf("expected cold start commands %v, got %v", expectedCold, recordedCommands)
	}
	cmdMu.Unlock()

	// 2. Repeated calls within 30s must NOT add any CLI calls
	advance(10 * time.Second)
	for range 5 {
		s2 := w.Status(context.Background())
		if s2.State != "connected" || s2.Version != "2026.9.1.0" {
			t.Fatalf("unexpected status in repeat: %+v", s2)
		}
	}
	cmdMu.Lock()
	if len(recordedCommands) != 4 {
		t.Fatalf("expected 0 additional commands within 30s, got %d total", len(recordedCommands))
	}
	cmdMu.Unlock()

	// 3. Dynamic expired (> 30s, e.g. at 35s): only status CLI call is added (1 call)
	advance(25 * time.Second) // total elapsed: 35s (> 30s dynamic TTL, < 5m metadata TTL)
	s3 := w.Status(context.Background())
	if s3.State != "connected" || s3.Version != "2026.9.1.0" {
		t.Fatalf("unexpected status after dynamic expiry: %+v", s3)
	}
	cmdMu.Lock()
	if len(recordedCommands) != 5 {
		t.Fatalf("expected 5 total commands after dynamic expiration, got %d: %v", len(recordedCommands), recordedCommands)
	}
	if recordedCommands[4] != "status" {
		t.Fatalf("expected 5th command to be 'status', got %q", recordedCommands[4])
	}
	cmdMu.Unlock()

	// 4. Metadata expired (> 5m, e.g. at 5m30s): metadata commands (3 calls) are added, dynamic is cached if within 30s
	advance(5 * time.Minute) // dynamic also expires here, so both expire
	// Let's first test when ONLY metadata expires: advance by 4m40s from step 3 so metadata expires (total > 5m) but dynamic just refreshed
	// Instead, let's step explicitly:
	// Reset commands list to test pure metadata expiration:
	cmdMu.Lock()
	recordedCommands = nil
	cmdMu.Unlock()

	// Call status now (both expired): should run all 4 commands
	_ = w.Status(context.Background())
	cmdMu.Lock()
	if len(recordedCommands) != 4 {
		t.Fatalf("expected 4 commands after full expiration, got %d: %v", len(recordedCommands), recordedCommands)
	}
	recordedCommands = nil
	cmdMu.Unlock()

	// Now advance dynamic by 15s (< 30s) and metadata by 5m:
	// Dynamic is fresh, but metadata expires!
	// To isolate metadata expiration: we can artificially set dynamicAt to now, metadataAt to 6m ago:
	w.dynamicMu.Lock()
	w.dynamicAt = getNow()
	w.dynamicMu.Unlock()
	w.metadataMu.Lock()
	w.metadataAt = getNow().Add(-6 * time.Minute)
	w.metadataMu.Unlock()

	_ = w.Status(context.Background())
	cmdMu.Lock()
	if len(recordedCommands) != 3 {
		t.Fatalf("expected only 3 metadata commands when only metadata expired, got %d: %v", len(recordedCommands), recordedCommands)
	}
	expectedMeta := []string{"--version", "settings", "registration show"}
	if !reflect.DeepEqual(recordedCommands, expectedMeta) {
		t.Fatalf("expected metadata commands %v, got %v", expectedMeta, recordedCommands)
	}
	recordedCommands = nil
	cmdMu.Unlock()

	// 5. InvalidateStatus forces both caches to refresh on next Status (4 calls)
	w.InvalidateStatus()
	_ = w.Status(context.Background())
	cmdMu.Lock()
	if len(recordedCommands) != 4 {
		t.Fatalf("expected 4 commands after InvalidateStatus, got %d: %v", len(recordedCommands), recordedCommands)
	}
	cmdMu.Unlock()
}

func TestWARPMetadataLastGoodPreservation(t *testing.T) {
	w := newWARP(config.Outbound{ID: "warp", ProxyAddress: "127.0.0.1:40000"})
	w.dialContext = func(_ context.Context, _, _ string) (net.Conn, error) {
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, nil
	}

	failMetadata := false
	var mu sync.Mutex
	w.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		mu.Lock()
		fail := failMetadata
		mu.Unlock()
		cmd := strings.Join(args[1:], " ")
		switch cmd {
		case "--version":
			if fail {
				return nil, errors.New("daemon busy")
			}
			return []byte("WARP 2026.9.1.0"), nil
		case "settings":
			if fail {
				return nil, errors.New("daemon busy")
			}
			return []byte("Mode: WarpProxy on port 40000"), nil
		case "registration show":
			if fail {
				return nil, errors.New("daemon busy")
			}
			return []byte("Account type: Unlimited"), nil
		case "status":
			return []byte("Status update: Connected"), nil
		default:
			return nil, errors.New("unknown")
		}
	}

	// First status succeeds
	s1 := w.Status(context.Background())
	if s1.Version != "2026.9.1.0" || s1.Account != "Unlimited" {
		t.Fatalf("initial status failed: %+v", s1)
	}

	// Invalidate metadata and make metadata commands fail
	mu.Lock()
	failMetadata = true
	mu.Unlock()

	w.metadataMu.Lock()
	w.metadataAt = time.Time{}
	w.metadataMu.Unlock()

	s2 := w.Status(context.Background())
	// Last good metadata should be preserved!
	if s2.Version != "2026.9.1.0" || s2.Mode != "proxy" || s2.Account != "Unlimited" {
		t.Fatalf("expected last-good metadata to be preserved, got: %+v", s2)
	}
}

func TestWARPConcurrentSingleflight(t *testing.T) {
	w := newWARP(config.Outbound{ID: "warp", ProxyAddress: "127.0.0.1:40000"})
	w.dialContext = func(_ context.Context, _, _ string) (net.Conn, error) {
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, nil
	}

	var statusCalls atomic.Int64
	var versionCalls atomic.Int64
	w.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		time.Sleep(10 * time.Millisecond) // artificially make it take time to increase concurrency window
		cmd := strings.Join(args[1:], " ")
		switch cmd {
		case "--version":
			versionCalls.Add(1)
			return []byte("WARP 2026.9.1.0"), nil
		case "settings":
			return []byte("Mode: WarpProxy on port 40000"), nil
		case "registration show":
			return []byte("Account type: Unlimited"), nil
		case "status":
			statusCalls.Add(1)
			return []byte("Status update: Connected"), nil
		default:
			return nil, errors.New("unknown")
		}
	}

	// Run 10 concurrent Status calls on cold start
	const concurrency = 10
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for range concurrency {
		go func() {
			defer wg.Done()
			s := w.Status(context.Background())
			if s.State != "connected" || s.Version != "2026.9.1.0" {
				t.Errorf("unexpected status: %+v", s)
			}
		}()
	}
	wg.Wait()

	// With singleflight, only 1 status call and 1 version call should have executed
	if got := statusCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 status CLI call across concurrent callers, got %d", got)
	}
	if got := versionCalls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 version CLI call across concurrent callers, got %d", got)
	}
}

func TestWARPActionInvalidatesCaches(t *testing.T) {
	w := newWARP(config.Outbound{ID: "warp", ProxyAddress: "127.0.0.1:40000"})
	w.dialContext = func(_ context.Context, _, _ string) (net.Conn, error) {
		c1, c2 := net.Pipe()
		_ = c2.Close()
		return c1, nil
	}

	var cmdCount atomic.Int64
	w.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		cmdCount.Add(1)
		cmd := strings.Join(args[1:], " ")
		switch cmd {
		case "--version":
			return []byte("WARP 2026.9.1.0"), nil
		case "settings":
			return []byte("Mode: WarpProxy on port 40000"), nil
		case "registration show":
			return []byte("Account type: Unlimited"), nil
		case "status":
			return []byte("Status update: Connected"), nil
		default:
			return []byte("ok"), nil
		}
	}

	// 1. Initial status: 4 commands
	_ = w.Status(context.Background())
	if got := cmdCount.Load(); got != 4 {
		t.Fatalf("expected 4 commands on initial status, got %d", got)
	}

	// 2. Immediate status without action: 0 additional commands
	_ = w.Status(context.Background())
	if got := cmdCount.Load(); got != 4 {
		t.Fatalf("expected 0 additional commands on cached status, got %d", got)
	}

	// 3. Action runs: invalidates both caches
	_ = w.Action(context.Background(), "version", "")

	// 4. Next status: must execute 4 commands again
	beforeStatus := cmdCount.Load()
	_ = w.Status(context.Background())
	afterStatus := cmdCount.Load()
	if afterStatus-beforeStatus != 4 {
		t.Fatalf("expected 4 commands after action invalidation, got %d", afterStatus-beforeStatus)
	}
}
