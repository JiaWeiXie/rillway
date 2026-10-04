//go:build live

// Package live contains opt-in tests against the user's explicitly selected VPN
// configuration. No account registration or VPN connect actions are performed.
package live

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/outbound"
	"testing"
	"time"
)

func TestConfiguredOutbounds(t *testing.T) {
	path := os.Getenv("RILLWAY_LIVE_CONFIG")
	if path == "" {
		t.Skip("NOT VERIFIED: set RILLWAY_LIVE_CONFIG to an explicit existing configuration to test real VPN accounts")
	}
	if !filepath.IsAbs(path) {
		t.Fatal("RILLWAY_LIVE_CONFIG must be an absolute path")
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// An existing tsnet login state is required. Do not inherit credentials that
	// could register another node as an unintended side effect of a test.
	for _, name := range []string{"TS_AUTHKEY", "TS_AUTH_KEY", "TS_CLIENT_SECRET", "TS_CLIENT_ID"} {
		t.Setenv(name, "")
	}
	selected, requestedTarget := os.Getenv("RILLWAY_LIVE_OUTBOUND"), os.Getenv("RILLWAY_LIVE_TARGET")
	if requestedTarget != "" {
		if host, port, e := net.SplitHostPort(requestedTarget); e != nil || host == "" || port == "" {
			t.Fatal("RILLWAY_LIVE_TARGET must be host:port, not a URL")
		}
	}
	count := 0
	for _, original := range cfg.Outbounds {
		if !original.Enabled || selected != "" && original.ID != selected {
			continue
		}
		count++
		t.Run(original.ID, func(t *testing.T) {
			o := original
			target := requestedTarget
			if target == "" {
				if o.Type == "tailscale" || o.Type == "wireguard" && !o.PublicInternet {
					t.Skip("NOT VERIFIED: private outbound requires RILLWAY_LIVE_TARGET=host:port")
				}
				target = "github.com:443"
			}
			switch o.Type {
			case "wireguard":
				if _, e := os.Stat(o.ConfigFile); os.IsNotExist(e) {
					t.Skip("NOT VERIFIED: WireGuard credential file is missing")
				}
			case "tailscale":
				if _, e := os.Stat(filepath.Join(o.StateDir, "tailscaled.state")); e != nil {
					t.Skip("NOT VERIFIED: an existing dedicated tsnet login state is required; tests do not register nodes")
				}
				o.AuthKeyFile = ""
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			provider, e := outbound.New(ctx, o)
			if e != nil {
				t.Fatal(e)
			}
			defer func() {
				if e := provider.Close(); e != nil {
					t.Error(e)
				}
			}()
			state := provider.Status(ctx)
			switch o.Type {
			case "warp":
				if state.Account == "" {
					t.Skip("NOT VERIFIED: WARP registration/client is unavailable; test does not register or connect WARP")
				}
				if state.Mode != "proxy" || !state.Listener {
					t.Fatal("registered WARP must already be in Local Proxy mode with a reachable listener")
				}
			case "tailscale":
				ticker := time.NewTicker(250 * time.Millisecond)
				defer ticker.Stop()
				for state.State == "Starting" || state.State == "NoState" {
					select {
					case <-ctx.Done():
						t.Fatal("embedded Tailscale did not become ready")
					case <-ticker.C:
						state = provider.Status(ctx)
					}
				}
				if state.State == "NeedsLogin" || state.State == "NeedsMachineAuth" {
					t.Skip("NOT VERIFIED: existing Tailscale state needs user authentication")
				}
				if state.State != "Running" {
					t.Fatalf("Tailscale state is %s", state.State)
				}
			}
			connection, e := provider.DialContext(ctx, "tcp", target)
			if e != nil {
				t.Fatal(e)
			}
			if e = connection.Close(); e != nil {
				t.Fatal(e)
			}
			t.Log("configured outbound completed a real TCP connection")
			if o.Type == "warp" {
				controller, ok := provider.(outbound.Controller)
				if !ok {
					t.Fatal("WARP provider has no verification controller")
				}
				if e = controller.Action(ctx, "verify", ""); e != nil {
					t.Fatal(e)
				}
				t.Run("warp-plus", func(t *testing.T) {
					verified := provider.Status(ctx)
					if verified.Account != "Unlimited" {
						t.Skip("NOT VERIFIED: WARP tunnel works, but this registration has no confirmed WARP+ Unlimited subscription")
					}
					if verified.VerifiedAt.IsZero() {
						t.Fatal("Unlimited subscription alone does not prove a working WARP tunnel")
					}
					t.Log("WARP+ Unlimited registration and proxied Cloudflare tunnel trace both verified")
				})
			}
		})
	}
	if count == 0 {
		t.Skip("NOT VERIFIED: no enabled outbound matches RILLWAY_LIVE_OUTBOUND")
	}
}
