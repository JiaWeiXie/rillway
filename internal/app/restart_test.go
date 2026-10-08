package app

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/control"
	"rillway/internal/platform"
	"testing"
	"time"
)

func TestWebRestartReloadsSavedConfigAndReleasesListeners(t *testing.T) {
	c := config.Default(t.TempDir())
	c.Listeners = config.Listeners{HTTP: freeAddress(t), SOCKS5: freeAddress(t), Admin: freeAddress(t), PAC: freeAddress(t)}
	c.PAC.ProxyAddress = c.Listeners.HTTP
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	token, _, err := platform.EnsureCredentials(c)
	if err != nil {
		t.Fatal(err)
	}
	client, err := control.NewClient("https://"+c.Listeners.Admin, token, c.Security.TLSCertFile)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready, done := make(chan struct{}, 3), make(chan error, 1)
	go func() { done <- Serve(ctx, path, c, func(string) { ready <- struct{}{} }) }()
	waitReady := func() {
		t.Helper()
		select {
		case <-ready:
		case err := <-done:
			t.Fatalf("service stopped: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("service did not become ready")
		}
	}
	waitReady()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("service did not stop")
		}
	}()
	// Ordinary runtime saves cannot change listener/security fields. A restart
	// loads an independently saved valid configuration without any privileges.
	c.Revision++
	c.PAC.BypassDomains = append(c.PAC.BypassDomains, config.PACBypass{Value: "corp.example", Enabled: true})
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	if err := client.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	waitReady()
	got, err := client.Config(ctx)
	if err != nil || got.Revision != c.Revision || len(got.PAC.BypassDomains) != len(c.PAC.BypassDomains) {
		t.Fatalf("restart did not reload saved settings: %v", err)
	}
	// The prior token still works and a second restart uses the same listeners.
	if err := client.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	waitReady()
	// Reject unavailable ports before disrupting the running listener set.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }()
	blocked := c
	blocked.Listeners.PAC = occupied.Addr().String()
	if err := config.Save(path, blocked); err != nil {
		t.Fatal(err)
	}
	if err := client.Restart(ctx); err == nil {
		t.Fatal("occupied listener accepted for restart")
	}
	if _, err := client.Config(ctx); err != nil {
		t.Fatalf("failed preflight stopped the service: %v", err)
	}
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(c.Security.TLSKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Security.TLSKeyFile, []byte("invalid credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.Restart(ctx); err == nil {
		t.Fatal("invalid credentials accepted for restart")
	}
	if err := os.WriteFile(c.Security.TLSKeyFile, key, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid configuration"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.Restart(ctx); err == nil {
		t.Fatal("invalid configuration accepted for restart")
	}
	if _, err := client.Config(ctx); err != nil {
		t.Fatalf("rejected restart stopped the current service: %v", err)
	}
}

func TestPrepareRestartAllowsRebindOnHeldPort(t *testing.T) {
	c := config.Default(t.TempDir())
	c.Listeners = config.Listeners{HTTP: freeAddress(t), SOCKS5: freeAddress(t), Admin: freeAddress(t), PAC: freeAddress(t)}
	c.PAC.ProxyAddress = c.Listeners.HTTP
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	if _, _, err := platform.EnsureCredentials(c); err != nil {
		t.Fatal(err)
	}
	r, err := New(t.Context(), path, c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	r.restart = make(chan struct{}, 1)
	held, err := net.Listen("tcp", c.Listeners.HTTP)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	_, port, err := net.SplitHostPort(c.Listeners.HTTP)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		host      string
		wantError bool
	}{
		{"0.0.0.0", false}, {"192.0.2.55", true},
	} {
		t.Run(tt.host, func(t *testing.T) {
			c.Listeners.HTTP = net.JoinHostPort(tt.host, port)
			if err := config.Save(path, c); err != nil {
				t.Fatal(err)
			}
			_, err := r.PrepareRestart(t.Context())
			if (err != nil) != tt.wantError {
				t.Fatalf("PrepareRestart error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
	occupiedAddress := net.JoinHostPort("127.0.0.2", port)
	occupied, err := net.Listen("tcp", occupiedAddress)
	if err != nil {
		t.Skipf("secondary loopback unavailable: %v", err)
	}
	defer func() { _ = occupied.Close() }()
	c.Listeners.HTTP = occupiedAddress
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PrepareRestart(t.Context()); err == nil {
		t.Fatal("restart accepted same-port address held by another listener")
	}
}

func TestRestartRestoresPreviousListenersAfterWildcardBindFailure(t *testing.T) {
	c := config.Default(t.TempDir())
	c.Listeners = config.Listeners{HTTP: freeAddress(t), SOCKS5: freeAddress(t), Admin: freeAddress(t), PAC: freeAddress(t)}
	c.PAC.ProxyAddress = c.Listeners.HTTP
	_, port, err := net.SplitHostPort(c.Listeners.HTTP)
	if err != nil {
		t.Fatal(err)
	}
	occupied, err := net.Listen("tcp", net.JoinHostPort("127.0.0.2", port))
	if err != nil {
		t.Skipf("secondary loopback unavailable: %v", err)
	}
	defer func() { _ = occupied.Close() }()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	token, _, err := platform.EnsureCredentials(c)
	if err != nil {
		t.Fatal(err)
	}
	client, err := control.NewClient("https://"+c.Listeners.Admin, token, c.Security.TLSCertFile)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	ready, done := make(chan struct{}, 2), make(chan error, 1)
	go func() { done <- Serve(ctx, path, c, func(string) { ready <- struct{}{} }) }()
	waitReady := func() {
		t.Helper()
		select {
		case <-ready:
		case err := <-done:
			t.Fatalf("service stopped: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("service did not become ready")
		}
	}
	waitReady()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("service did not stop")
		}
	}()
	next := c
	next.Listeners.HTTP = net.JoinHostPort("0.0.0.0", port)
	if err := config.Save(path, next); err != nil {
		t.Fatal(err)
	}
	if err := client.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	waitReady()
	if _, err := client.Config(ctx); err != nil {
		t.Fatalf("failed wildcard bind did not restore previous listeners: %v", err)
	}
}

func TestServeLoopRestoresActiveConfigAfterRestartFailure(t *testing.T) {
	startup := config.Default(t.TempDir())
	startup.Revision = 1
	active := startup
	active.Revision = 2
	next := active
	next.Revision = 3
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, next); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls int
	err := serveLoop(ctx, path, startup, nil, serverTimeouts{}, func(_ context.Context, _ string, got config.Config, _ func(string), _ serverTimeouts) error {
		calls++
		switch calls {
		case 1:
			if got.Revision != startup.Revision {
				t.Fatalf("startup revision = %d", got.Revision)
			}
			return restartRequest{active: active}
		case 2:
			if got.Revision != next.Revision {
				t.Fatalf("restart revision = %d", got.Revision)
			}
			return errors.New("new listener bind failed")
		case 3:
			if got.Revision != active.Revision {
				t.Fatalf("fallback revision = %d, want active %d", got.Revision, active.Revision)
			}
			cancel()
			return nil
		default:
			t.Fatalf("unexpected serve attempt %d", calls)
			return nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("serve attempts = %d, want 3", calls)
	}
}

func TestServeLoopClearsFallbackAfterRestartReady(t *testing.T) {
	startup := config.Default(t.TempDir())
	startup.Revision = 1
	active := startup
	active.Revision = 2
	next := active
	next.Revision = 3
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, next); err != nil {
		t.Fatal(err)
	}
	runtimeErr := errors.New("listener failed after readiness")
	var calls int
	err := serveLoop(t.Context(), path, startup, nil, serverTimeouts{}, func(_ context.Context, _ string, got config.Config, ready func(string), _ serverTimeouts) error {
		calls++
		switch calls {
		case 1:
			return restartRequest{active: active}
		case 2:
			if got.Revision != next.Revision {
				t.Fatalf("restart revision = %d", got.Revision)
			}
			ready("ready")
			return runtimeErr
		default:
			t.Fatalf("stale fallback restarted after ready; attempt %d revision %d", calls, got.Revision)
			return nil
		}
	})
	if !errors.Is(err, runtimeErr) {
		t.Fatalf("serve error = %v, want %v", err, runtimeErr)
	}
	if calls != 2 {
		t.Fatalf("serve attempts = %d, want 2", calls)
	}
}
