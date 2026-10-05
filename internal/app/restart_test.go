package app

import (
	"context"
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
