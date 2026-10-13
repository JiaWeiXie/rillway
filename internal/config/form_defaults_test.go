package config

import (
	"path/filepath"
	"reflect"
	"slices"
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
	c.Outbounds = slices.Clone(c.Outbounds)
	c.OutboundGroups = slices.Clone(c.OutboundGroups)
	c.RuleSets = slices.Clone(c.RuleSets)
	c.Rules = slices.Clone(c.Rules)
	return c
}

func TestFormDefaultsIncludesGroupAndRuleSet(t *testing.T) {
	c := Default(t.TempDir())
	c.Outbounds[1].Enabled = true
	c.Outbounds = append(
		c.Outbounds,
		Outbound{ID: "disabled", Type: "warp", ProxyAddress: "127.0.0.1:40001"},
		Outbound{ID: "direct-extra", Type: "direct", Enabled: true, PublicInternet: true},
		Outbound{ID: "vpn-b", Type: "warp", Enabled: true, PublicInternet: true, ProxyAddress: "127.0.0.1:40002"},
		Outbound{ID: "vpn-c", Type: "warp", Enabled: true, PublicInternet: true, ProxyAddress: "127.0.0.1:40003"},
	)
	if err := Validate(c); err != nil {
		t.Fatalf("invalid baseline: %v", err)
	}
	before := phase1JSON(t, c)
	d := DefaultsForForms(c)
	if string(phase1JSON(t, c)) != string(before) {
		t.Fatal("suggestions mutated input")
	}
	wantGroup := OutboundGroup{ID: "group-1", Type: "fallback", Enabled: true, Members: []string{"warp", "vpn-b"}}
	wantSet := RuleSet{ID: "ruleset-1", Name: "New rule set", Domains: []string{"example.com"}}
	if !reflect.DeepEqual(d.Group, wantGroup) || !reflect.DeepEqual(d.RuleSet, wantSet) {
		t.Fatalf("unexpected defaults: %+v / %+v", d.Group, d.RuleSet)
	}
	if len(d.Rule.Sources)+len(d.Rule.Inbounds)+len(d.Rule.Ports)+len(d.Rule.Sets) != 0 {
		t.Fatal("rule defaults invented composite conditions")
	}
	next := cloneForDefaultsTest(c)
	next.OutboundGroups = append(next.OutboundGroups, d.Group)
	next.RuleSets = append(next.RuleSets, d.RuleSet)
	if err := Validate(next); err != nil {
		t.Fatalf("invalid suggestions: %v", err)
	}
	c = next
	before = phase1JSON(t, c)
	d = DefaultsForForms(c)
	if d.Group.ID != "group-2" || d.RuleSet.ID != "ruleset-2" {
		t.Fatal("suggested IDs collide with existing entities")
	}
	if string(phase1JSON(t, c)) != string(before) {
		t.Fatal("collision handling mutated input")
	}
	// All entity kinds share the namespace.
	c.Outbounds = append(c.Outbounds, Outbound{ID: "group-2", Type: "direct", Enabled: true, PublicInternet: true})
	c.RuleSets = append(c.RuleSets, RuleSet{ID: "group-3", Name: "Occupied", Domains: []string{"example.com"}})
	c.OutboundGroups = append(c.OutboundGroups, OutboundGroup{ID: "ruleset-2", Type: "fallback", Enabled: true, Members: []string{"warp"}})
	if err := Validate(c); err != nil {
		t.Fatalf("invalid collision baseline: %v", err)
	}
	d = DefaultsForForms(c)
	if d.Group.ID != "group-4" || d.RuleSet.ID != "ruleset-3" {
		t.Fatalf("cross-collection collision: %+v / %+v", d.Group, d.RuleSet)
	}
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
