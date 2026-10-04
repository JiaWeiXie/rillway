package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestFormDefaultsUseDaemonPathsAndAvoidDuplicateIDs(t *testing.T) {
	state := filepath.Join(t.TempDir(), "server-state")
	c := Default(state)
	c.Outbounds = append(c.Outbounds, Outbound{ID: "warp-2", Type: "warp", ProxyAddress: "127.0.0.1:40000"}, Outbound{ID: "tailscale", Type: "tailscale", StateDir: filepath.Join(state, "other-node")})
	c.Rules = append(c.Rules, Rule{ID: "rule", Domains: []string{"example.com"}, Outbound: "direct"})
	original := cloneForDefaultsTest(c)
	d := DefaultsForForms(c)
	if !reflect.DeepEqual(c, original) {
		t.Fatal("suggestions modified current configuration")
	}
	if d.Outbounds["warp"].ID != "warp-3" || d.Outbounds["warp"].ProxyAddress != "127.0.0.1:40000" || d.Outbounds["warp"].WARPBinary != "warp-cli" {
		t.Fatal("WARP defaults are missing or collide")
	}
	if d.Outbounds["tailscale"].StateDir != filepath.Join(state, "tailscale", "tailscale-2") || d.Outbounds["tailscale"].PublicInternet || d.Outbounds["tailscale"].Enabled {
		t.Fatal("Tailscale must have a dedicated private state path and require explicit enable")
	}
	if d.Outbounds["wireguard"].Enabled || d.Outbounds["wireguard"].ConfigFile != filepath.Join(state, "secrets", "wireguard.conf") {
		t.Fatal("WireGuard defaults must not invent keys or enable an unconfigured VPN")
	}
	if d.Rule.ID != "rule-2" || d.Rule.Outbound != c.DefaultOutbound || d.Rule.Family != "auto" {
		t.Fatal("rule defaults did not preserve current route and automatic IP choice")
	}
	for kind, o := range d.Outbounds {
		next := cloneForDefaultsTest(c)
		next.Outbounds = append(next.Outbounds, o)
		if err := Validate(next); err != nil {
			t.Fatalf("%s suggested profile invalid: %v", kind, err)
		}
		if o.AuthKeyFile != "" {
			t.Fatal("suggestions must not include auth keys")
		}
	}
}

func cloneForDefaultsTest(c Config) Config {
	c.Outbounds = append([]Outbound(nil), c.Outbounds...)
	c.Rules = append([]Rule(nil), c.Rules...)
	return c
}

func TestFormDefaultsAvoidOccupiedTailscaleState(t *testing.T) {
	state := t.TempDir()
	c := Default(state)
	c.Outbounds = append(c.Outbounds, Outbound{ID: "company", Type: "tailscale", StateDir: filepath.Join(state, "tailscale", "tailscale")})
	d := DefaultsForForms(c).Outbounds["tailscale"]
	if d.ID != "tailscale-2" || d.StateDir == c.Outbounds[2].StateDir {
		t.Fatal("suggested state already belongs to another node")
	}
}
