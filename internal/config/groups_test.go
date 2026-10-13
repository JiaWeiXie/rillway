package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func phase1JSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func phase1GroupConfig(t *testing.T) Config {
	t.Helper()
	c := Default(t.TempDir())
	c.Outbounds[1].Enabled = true
	c.Outbounds = append(
		c.Outbounds,
		Outbound{ID: "vpn-b", Type: "warp", Enabled: true, PublicInternet: true, ProxyAddress: "127.0.0.1:40001", WARPBinary: "warp-cli"},
		Outbound{ID: "direct-extra", Type: "direct", Enabled: true, PublicInternet: true},
	)
	c.OutboundGroups = []OutboundGroup{{ID: "group-1", Type: "fallback", Enabled: true, Members: []string{"warp", "vpn-b"}}}
	c.RuleSets = []RuleSet{{ID: "set-1", Name: "Example set", Domains: []string{"example.com"}}}
	if err := Validate(c); err != nil {
		t.Fatalf("invalid test baseline: %v", err)
	}
	return c
}

func TestOutboundGroupsValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"direct member", func(c *Config) { c.OutboundGroups[0].Members[0] = "direct" }},
		{"direct alias member", func(c *Config) { c.OutboundGroups[0].Members[0] = "direct-extra" }},
		{"outbound collision", func(c *Config) { c.OutboundGroups[0].ID = "warp" }},
		{"duplicate group", func(c *Config) { c.OutboundGroups = append(c.OutboundGroups, c.OutboundGroups[0]) }},
		{"invalid ID", func(c *Config) { c.OutboundGroups[0].ID = "has space" }},
		{"empty ID", func(c *Config) { c.OutboundGroups[0].ID = "" }},
		{"long ID", func(c *Config) { c.OutboundGroups[0].ID = strings.Repeat("a", 65) }},
		{"unknown type", func(c *Config) { c.OutboundGroups[0].Type = "random" }},
		{"empty members", func(c *Config) { c.OutboundGroups[0].Members = nil }},
		{"missing member", func(c *Config) { c.OutboundGroups[0].Members[0] = "missing" }},
		{"nested member", func(c *Config) { c.OutboundGroups[0].Members[0] = "group-1" }},
		{"disabled member", func(c *Config) { c.Outbounds[1].Enabled = false }},
		{"duplicate member", func(c *Config) { c.OutboundGroups[0].Members = []string{"warp", "warp"} }},
		{"fallback strategy", func(c *Config) { c.OutboundGroups[0].Strategy = "round-robin" }},
		{"fallback key", func(c *Config) { c.OutboundGroups[0].Key = "source" }},
		{"fallback selected", func(c *Config) { c.OutboundGroups[0].Selected = "warp" }},
		{"select missing selected", func(c *Config) { c.OutboundGroups[0].Type = "select" }},
		{"select nonmember", func(c *Config) { c.OutboundGroups[0].Type = "select"; c.OutboundGroups[0].Selected = "direct" }},
		{"select strategy", func(c *Config) {
			c.OutboundGroups[0].Type = "select"
			c.OutboundGroups[0].Selected = "warp"
			c.OutboundGroups[0].Strategy = "round-robin"
		}},
		{"select key", func(c *Config) {
			c.OutboundGroups[0].Type = "select"
			c.OutboundGroups[0].Selected = "warp"
			c.OutboundGroups[0].Key = "source"
		}},
		{"load-balance missing strategy", func(c *Config) { c.OutboundGroups[0].Type = "load-balance" }},
		{"load-balance invalid strategy", func(c *Config) { c.OutboundGroups[0].Type = "load-balance"; c.OutboundGroups[0].Strategy = "random" }},
		{"round-robin key", func(c *Config) {
			c.OutboundGroups[0].Type = "load-balance"
			c.OutboundGroups[0].Strategy = "round-robin"
			c.OutboundGroups[0].Key = "source"
		}},
		{"hash missing key", func(c *Config) {
			c.OutboundGroups[0].Type = "load-balance"
			c.OutboundGroups[0].Strategy = "consistent-hashing"
		}},
		{"hash invalid key", func(c *Config) {
			c.OutboundGroups[0].Type = "load-balance"
			c.OutboundGroups[0].Strategy = "consistent-hashing"
			c.OutboundGroups[0].Key = "random"
		}},
		{"load-balance selected", func(c *Config) {
			c.OutboundGroups[0].Type = "load-balance"
			c.OutboundGroups[0].Strategy = "round-robin"
			c.OutboundGroups[0].Selected = "warp"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := phase1GroupConfig(t)
			tc.change(&c)
			before := phase1JSON(t, c)
			if err := Validate(c); err == nil {
				t.Fatal("invalid group accepted")
			}
			if string(phase1JSON(t, c)) != string(before) {
				t.Fatal("validation mutated input")
			}
		})
	}
	for _, group := range []OutboundGroup{
		{ID: "group-1", Type: "fallback", Enabled: true, Members: []string{"warp", "vpn-b"}},
		{ID: "group-1", Type: "select", Enabled: true, Members: []string{"warp", "vpn-b"}, Selected: "vpn-b"},
		{ID: "group-1", Type: "load-balance", Enabled: true, Members: []string{"warp", "vpn-b"}, Strategy: "round-robin"},
		{ID: "group-1", Type: "load-balance", Enabled: true, Members: []string{"warp", "vpn-b"}, Strategy: "consistent-hashing", Key: "destination"},
		{ID: "Group_2", Type: "load-balance", Enabled: true, Members: []string{"warp", "vpn-b"}, Strategy: "consistent-hashing", Key: "source"},
	} {
		t.Run(group.Type+"/"+group.Strategy+"/"+group.Key, func(t *testing.T) {
			c := phase1GroupConfig(t)
			c.OutboundGroups[0] = group
			c.DefaultOutbound = group.ID
			c.Rules[0].Outbound = group.ID
			if err := Validate(c); err != nil {
				t.Fatalf("valid group route rejected: %v", err)
			}
		})
	}
}

func TestRuleSetsValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"outbound collision", func(c *Config) { c.RuleSets[0].ID = "warp" }},
		{"group collision", func(c *Config) { c.RuleSets[0].ID = "group-1" }},
		{"duplicate", func(c *Config) { c.RuleSets = append(c.RuleSets, c.RuleSets[0]) }},
		{"invalid ID", func(c *Config) { c.RuleSets[0].ID = "has space" }},
		{"empty name", func(c *Config) { c.RuleSets[0].Name = "" }},
		{"long name", func(c *Config) { c.RuleSets[0].Name = strings.Repeat("a", 257) }},
		{"empty conditions", func(c *Config) { c.RuleSets[0].Domains = nil }},
		{"bad domain", func(c *Config) { c.RuleSets[0].Domains = []string{"example.com/bad"} }},
		{"bad suffix", func(c *Config) { c.RuleSets[0].Suffixes = []string{"bad*suffix"} }},
		{"bad CIDR", func(c *Config) { c.RuleSets[0].CIDRs = []string{"999.999.999.999/24"} }},
		{"bad source", func(c *Config) { c.RuleSets[0].Sources = []string{"203.0.113.1"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := phase1GroupConfig(t)
			tc.change(&c)
			before := phase1JSON(t, c)
			if err := Validate(c); err == nil {
				t.Fatal("invalid rule set accepted")
			}
			if string(phase1JSON(t, c)) != string(before) {
				t.Fatal("validation mutated input")
			}
		})
	}
	for _, set := range []RuleSet{
		{ID: "set-1", Name: strings.Repeat("a", 256), Domains: []string{"example.com"}},
		{ID: "set-1", Name: "Suffix", Suffixes: []string{"example.com"}},
		{ID: "set-1", Name: "CIDR", CIDRs: []string{"203.0.113.0/24", "2001:db8::/32"}},
		{ID: "set-1", Name: "Source", Sources: []string{"203.0.113.0/24", "2001:db8::/32"}},
	} {
		c := phase1GroupConfig(t)
		c.RuleSets[0] = set
		c.Rules = append(c.Rules, Rule{ID: "set-rule", Sets: []string{set.ID}, Outbound: "group-1"})
		if err := Validate(c); err != nil {
			t.Fatalf("valid set %q rejected: %v", set.Name, err)
		}
	}
}

func TestRemoveGroup(t *testing.T) {
	for _, replacement := range []string{"warp", "direct-extra", "group-2"} {
		t.Run("replace/"+replacement, func(t *testing.T) {
			c := phase1GroupConfig(t)
			c.OutboundGroups = append(c.OutboundGroups, OutboundGroup{ID: "group-2", Type: "select", Enabled: true, Members: []string{"vpn-b"}, Selected: "vpn-b"})
			c.DefaultOutbound = "group-1"
			c.Rules[0].Outbound = "group-1"
			c.Rules[0].Sources = []string{"203.0.113.0/24"}
			c.Rules[0].Inbounds = []string{"socks5"}
			c.Rules[0].Ports = []string{"443"}
			c.Rules[0].Sets = []string{"set-1"}
			before := phase1JSON(t, c)
			next, err := RemoveGroup(c, "group-1", replacement)
			if err != nil {
				t.Fatal(err)
			}
			if string(phase1JSON(t, c)) != string(before) {
				t.Fatal("successful removal mutated input")
			}
			want := c
			want.OutboundGroups = []OutboundGroup{c.OutboundGroups[1]}
			want.Rules = append([]Rule(nil), c.Rules...)
			want.DefaultOutbound = replacement
			want.Rules[0].Outbound = replacement
			if !reflect.DeepEqual(next, want) {
				t.Fatalf("removal changed unrelated settings: got %+v, want %+v", next, want)
			}
			if err := Validate(next); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, reference := range []string{"rule", "default", "both"} {
		for _, replacement := range []string{"", "missing", "group-1", "disabled-vpn", "disabled-group"} {
			t.Run(reference+"/reject/"+replacement, func(t *testing.T) {
				c := phase1GroupConfig(t)
				c.Outbounds = append(c.Outbounds, Outbound{ID: "disabled-vpn", Type: "warp", ProxyAddress: "127.0.0.1:40002"})
				c.OutboundGroups = append(c.OutboundGroups, OutboundGroup{ID: "disabled-group", Type: "fallback", Members: []string{"warp"}})
				if reference != "default" {
					c.Rules[0].Outbound = "group-1"
				}
				if reference != "rule" {
					c.DefaultOutbound = "group-1"
				}
				if err := Validate(c); err != nil {
					t.Fatalf("invalid baseline: %v", err)
				}
				before := phase1JSON(t, c)
				_, err := RemoveGroup(c, "group-1", replacement)
				if err == nil {
					t.Fatal("unsafe replacement accepted")
				}
				if replacement == "" && err.Error() != GroupReplacementRequired {
					t.Fatalf("wrong public error: %v", err)
				}
				if string(phase1JSON(t, c)) != string(before) {
					t.Fatal("rejected removal mutated input")
				}
			})
		}
	}
	t.Run("missing", func(t *testing.T) {
		c := phase1GroupConfig(t)
		before := phase1JSON(t, c)
		if _, err := RemoveGroup(c, "missing", "warp"); err == nil || err.Error() != GroupMissing {
			t.Fatalf("wrong missing error: %v", err)
		}
		if string(phase1JSON(t, c)) != string(before) {
			t.Fatal("missing removal mutated input")
		}
	})
	t.Run("unreferenced", func(t *testing.T) {
		c := phase1GroupConfig(t)
		before := phase1JSON(t, c)
		next, err := RemoveGroup(c, "group-1", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(next.OutboundGroups) != 0 || !reflect.DeepEqual(next.Rules, c.Rules) || next.DefaultOutbound != c.DefaultOutbound || next.Revision != c.Revision {
			t.Fatal("unreferenced removal changed routes or revision")
		}
		if string(phase1JSON(t, c)) != string(before) {
			t.Fatal("unreferenced removal mutated input")
		}
	})
}
