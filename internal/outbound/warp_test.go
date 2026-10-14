package outbound

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"rillway/internal/config"
	"rillway/internal/routeinfo"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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

func TestWARPPartialMetadataFailurePreservesCompleteSnapshot(t *testing.T) {
	for _, failed := range []string{"--version", "settings", "registration show"} {
		t.Run(failed, func(t *testing.T) {
			w := newWARP(config.Outbound{ID: "warp"})
			w.dialContext = func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("test listener unavailable")
			}
			refresh := false
			w.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				cmd := strings.Join(args[1:], " ")
				if refresh && cmd == failed {
					return []byte("private-license"), errors.New("private systemctl output")
				}
				switch cmd {
				case "status":
					return []byte("Connected"), nil
				case "--version":
					if refresh {
						return []byte("2026.10.2.0"), nil
					}
					return []byte("2026.9.1.0"), nil
				case "settings":
					if refresh {
						return []byte("Mode: Warp"), nil
					}
					return []byte("Mode: Proxy"), nil
				case "registration show":
					if refresh {
						return []byte("Account type: Free"), nil
					}
					return []byte("Account type: Unlimited"), nil
				default:
					return nil, errors.New("unexpected command")
				}
			}
			before := w.Status(t.Context())
			refresh = true
			w.InvalidateStatus()
			after := w.Status(t.Context())
			if after.Version != before.Version || after.Mode != before.Mode || after.Account != before.Account {
				t.Fatalf("partial refresh replaced complete metadata: before=%+v after=%+v", before, after)
			}
			if after.State != "connected" || strings.Contains(after.Detail, "private") {
				t.Fatalf("unsafe or incorrect status: %+v", after)
			}
		})
	}
}

func TestWARPInvalidationDuringInflightStatus(t *testing.T) {
	for _, blocked := range []string{"status", "--version"} {
		for _, finishOldFirst := range []bool{true, false} {
			name := blocked + "/refresh-before-completion"
			if finishOldFirst {
				name = blocked + "/completion-before-refresh"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				w := newWARP(config.Outbound{ID: "warp"})
				w.dialContext = func(context.Context, string, string) (net.Conn, error) {
					return nil, errors.New("test listener unavailable")
				}
				started, release := make(chan struct{}), make(chan struct{})
				var mu sync.Mutex
				calls := map[string]int{}
				w.run = func(ctx context.Context, _ string, args ...string) ([]byte, error) {
					cmd := strings.Join(args[1:], " ")
					mu.Lock()
					calls[cmd]++
					call := calls[cmd]
					mu.Unlock()
					if cmd == blocked && call == 1 {
						close(started)
						select {
						case <-release:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
					switch cmd {
					case "status":
						return []byte("Connected"), nil
					case "--version":
						if call == 1 {
							return []byte("2026.9.1.0"), nil
						}
						return []byte("2026.10.2.0"), nil
					case "settings":
						return []byte("Mode: Proxy"), nil
					case "registration show":
						return []byte("Account type: Unlimited"), nil
					default:
						return nil, errors.New("unexpected command")
					}
				}
				oldDone := make(chan struct{})
				go func() {
					defer close(oldDone)
					_ = w.Status(ctx)
				}()
				await := func(ch <-chan struct{}) {
					t.Helper()
					select {
					case <-ch:
					case <-ctx.Done():
						t.Fatal("timed out waiting for blocked status")
					}
				}
				await(started)
				w.InvalidateStatus()
				if finishOldFirst {
					close(release)
					await(oldDone)
				}
				mu.Lock()
				before := make(map[string]int, len(calls))
				for cmd, n := range calls {
					before[cmd] = n
				}
				mu.Unlock()
				fresh := w.Status(ctx)
				mu.Lock()
				for _, cmd := range []string{"status", "--version", "settings", "registration show"} {
					if calls[cmd] != before[cmd]+1 {
						t.Errorf("forced refresh did not execute %q: before=%d after=%d", cmd, before[cmd], calls[cmd])
					}
				}
				mu.Unlock()
				expectedVersion := "2026.10.2.0"
				if before["--version"] == 0 {
					expectedVersion = "2026.9.1.0"
				}
				if fresh.Version != expectedVersion || fresh.State != "connected" {
					t.Fatalf("forced refresh returned stale status: %+v", fresh)
				}
				if !finishOldFirst {
					close(release)
					await(oldDone)
				}
				mu.Lock()
				beforeCached := calls["status"] + calls["--version"] + calls["settings"] + calls["registration show"]
				mu.Unlock()
				cached := w.Status(ctx)
				mu.Lock()
				afterCached := calls["status"] + calls["--version"] + calls["settings"] + calls["registration show"]
				mu.Unlock()
				if cached.Version != fresh.Version || afterCached != beforeCached {
					t.Fatalf("late completion overwrote current epoch: cached=%+v commands=%d->%d", cached, beforeCached, afterCached)
				}
			})
		}
	}
}

func TestWARPDialPacingAndDualBudgetRetry(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	var mode atomic.Int32 // 0: transient resets then success, 1: always transient reset, 2: target ECONNREFUSED (code 5)
	var attempts atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				n := attempts.Add(1)
				var greet [3]byte
				if _, err := io.ReadFull(conn, greet[:]); err != nil {
					return
				}
				m := mode.Load()
				if m == 0 && n == 1 {
					// Simulate greeting EOF during tunnel reset.
					return
				}
				if _, err := conn.Write([]byte{5, 0}); err != nil {
					return
				}
				buf := make([]byte, 64)
				if _, err := conn.Read(buf); err != nil {
					return
				}
				switch m {
				case 0:
					switch n {
					case 2:
						// Simulate a transient reply EOF during tunnel teardown.
						return
					case 3, 4:
						// A SOCKS5 general failure (code 1) can be transient.
						_, _ = conn.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
						return
					}
				case 1:
					// Always fail with SOCKS5 general failure (code 1).
					_, _ = conn.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				case 2:
					// Target server refused connection (code 5 -> ECONNREFUSED, non-retryable).
					_, _ = conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 1, 187})
				_, _ = io.Copy(io.Discard, conn)
			}(c)
		}
	}()

	fakeNow := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	w := newWARP(config.Outbound{ID: "warp", ProxyAddress: ln.Addr().String()})
	w.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return fakeNow
	}
	w.sleep = func(ctx context.Context, d time.Duration) error {
		clockMu.Lock()
		fakeNow = fakeNow.Add(d)
		clockMu.Unlock()
		return ctx.Err()
	}

	// 1. Fixed route (FastFailover == false) rides out 4 transient failures and succeeds on attempt 5.
	mode.Store(0)
	attempts.Store(0)
	conn, err := w.DialContext(t.Context(), "tcp", "chatgpt.com:443")
	if err != nil {
		t.Fatalf("fixed WARP route failed during transient reset window: %v", err)
	}
	_ = conn.Close()
	if got := attempts.Load(); got != 5 {
		t.Fatalf("expected 5 attempts for fixed route recovery, got %d", got)
	}

	// 2. Adaptive FastFailover route (FastFailover == true) retries only once (2 attempts total) and returns ErrWARPReset.
	mode.Store(1)
	attempts.Store(0)
	fastCtx := routeinfo.With(t.Context(), routeinfo.Info{FastFailover: true})
	_, err = w.DialContext(fastCtx, "tcp", "chatgpt.com:443")
	if !errors.Is(err, ErrWARPReset) {
		t.Fatalf("expected ErrWARPReset on fast failover route, got %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("expected 2 attempts (1 initial + 1 fast retry) for FastFailover route, got %d", got)
	}

	// 3. Target-specific ECONNREFUSED (SOCKS5 code 5) does not retry at all.
	mode.Store(2)
	attempts.Store(0)
	_, err = w.DialContext(t.Context(), "tcp", "chatgpt.com:443")
	if !errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, ErrWARPReset) {
		t.Fatalf("expected non-WARP-reset ECONNREFUSED, got %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("expected 1 attempt for non-retryable target refusal, got %d", got)
	}

	// 4. Burst pacing and immediate handshake slot release: 12 concurrent open streams succeed even though handshakeSem cap is 8.
	mode.Store(0)
	attempts.Store(10) // >= 5 so server immediately succeeds
	clockMu.Lock()
	w.burstTokens = 4
	w.lastDial = fakeNow
	clockMu.Unlock()

	conns := make([]net.Conn, 12)
	var wg sync.WaitGroup
	for i := range conns {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c, dialErr := w.DialContext(t.Context(), "tcp", "ws.chatgpt.com:443")
			if dialErr != nil {
				t.Errorf("concurrent dial %d failed: %v", idx, dialErr)
				return
			}
			conns[idx] = c
		}(i)
	}
	wg.Wait()
	for _, c := range conns {
		if c != nil {
			_ = c.Close()
		}
	}
}

func TestWARPTokenBucketPacesAdmissions(t *testing.T) {
	w := newWARP(config.Outbound{ID: "warp"})
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	start := now
	w.now = func() time.Time { return now }
	w.sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		now = now.Add(d)
		return nil
	}
	for i := range 12 {
		release, err := w.acquireHandshakeSlot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		elapsed := now.Sub(start)
		minimum := time.Duration(max(0, i-3)) * w.paceInterval
		if elapsed != minimum {
			t.Fatalf("admission %d at %v, want %v with burst=4 and interval=%v", i+1, elapsed, minimum, w.paceInterval)
		}
		release()
	}
}

func TestWARPRetrySleepCancellationReturnsContextError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var attempts atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			attempts.Add(1)
			func() {
				defer func() { _ = conn.Close() }()
				var greeting [3]byte
				if _, err := io.ReadFull(conn, greeting[:]); err != nil {
					return
				}
				_, _ = conn.Write([]byte{5, 0})
				var request [18]byte // SOCKS5 domain request for "example.com:443"
				if _, err := io.ReadFull(conn, request[:]); err != nil {
					return
				}
				_, _ = conn.Write([]byte{5, 1, 0, 1, 0, 0, 0, 0, 0, 0})
			}()
		}
	}()
	w := newWARP(config.Outbound{ID: "warp", ProxyAddress: ln.Addr().String()})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	_, err = w.DialContext(ctx, "tcp", "example.com:443")
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrWARPReset) {
		t.Fatalf("retry cancellation must not be reported as proxy reset: %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("expected one handshake before cancellation, got %d", got)
	}
}

func TestWARPCanceledPacingDoesNotReserveFutureSlots(t *testing.T) {
	w := newWARP(config.Outbound{ID: "warp"})
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	for range 4 {
		release, err := w.acquireHandshakeSlot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	for range 2 {
		ctx, cancel := context.WithCancel(t.Context())
		w.sleep = func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		}
		release, err := w.acquireHandshakeSlot(ctx)
		if release != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled pacing waiter acquired a slot: release=%v err=%v", release != nil, err)
		}
		cancel()
	}
	now = now.Add(w.paceInterval)
	w.sleep = func(context.Context, time.Duration) error {
		return errors.New("unnecessary wait after canceled admission")
	}
	release, err := w.acquireHandshakeSlot(t.Context())
	if err != nil {
		t.Fatalf("canceled waiters reserved future admission time: %v", err)
	}
	release()
}

func TestWARPHandshakeSlotsReleaseOnCancellation(t *testing.T) {
	w := newWARP(config.Outbound{ID: "warp"})
	w.paceInterval = 0
	// docs/providers.md publishes this limit; keep behavior and documentation together.
	if cap(w.handshakeSem) != 8 {
		t.Fatalf("handshake concurrency cap = %d, want documented 8", cap(w.handshakeSem))
	}
	releases := make([]func(), cap(w.handshakeSem))
	for i := range releases {
		var err error
		releases[i], err = w.acquireHandshakeSlot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	entered := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(entered)
		release, err := w.acquireHandshakeSlot(ctx)
		if release != nil {
			release()
		}
		result <- err
	}()
	<-entered
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked ninth handshake was not canceled: %v", err)
	}
	releases[0]()
	next, err := w.acquireHandshakeSlot(t.Context())
	if err != nil {
		t.Fatalf("canceled waiter left the semaphore occupied: %v", err)
	}
	next()
	for _, release := range releases[1:] {
		release()
	}
}

func TestWARPSOCKSReplyTailReset(t *testing.T) {
	for _, tc := range []struct {
		name string
		tail []byte
	}{
		{"domain length", []byte{5, 0, 0, 3}},
		{"domain address and port", []byte{5, 0, 0, 3, 5, 'a'}},
		{"IPv4 address and port", []byte{5, 0, 0, 1, 127, 0}},
		{"IPv6 address and port", []byte{5, 0, 0, 4, 0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ln.Close() }()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				var greeting [3]byte
				if _, err := io.ReadFull(conn, greeting[:]); err != nil {
					return
				}
				_, _ = conn.Write([]byte{5, 0})
				var request [18]byte
				if _, err := io.ReadFull(conn, request[:]); err != nil {
					return
				}
				_, _ = conn.Write(tc.tail)
			}()
			_, err = dialSOCKS(t.Context(), ln.Addr().String(), "tcp", "example.com:443")
			<-done
			if !errors.Is(err, ErrWARPReset) || !isTransientSOCKSError(err) {
				t.Fatalf("truncated successful SOCKS reply must be retryable: %v", err)
			}
		})
	}
}

func TestWARPSOCKSHandshakeCancellationIsNotAReset(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var greeting [3]byte
		if _, err := io.ReadFull(conn, greeting[:]); err != nil {
			return
		}
		_, _ = conn.Write([]byte{5, 0})
		var request [18]byte
		if _, err := io.ReadFull(conn, request[:]); err != nil {
			return
		}
		_, _ = conn.Write([]byte{5, 0, 0, 3})
		cancel()
	}()
	_, err = dialSOCKS(ctx, ln.Addr().String(), "tcp", "example.com:443")
	<-done
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrWARPReset) {
		t.Fatalf("canceled handshake must preserve context cancellation: %v", err)
	}
}
