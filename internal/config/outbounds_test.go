package config

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

func TestDirectIsRequiredAndProtected(t *testing.T) {
	for _, modify := range []func(*Config){
		func(c *Config) { c.Outbounds = c.Outbounds[1:] },
		func(c *Config) { c.Outbounds[0].ID = "renamed"; c.DefaultOutbound = "renamed" },
		func(c *Config) {
			c.Outbounds[0].Enabled = false
			c.Outbounds[1].Enabled = true
			c.DefaultOutbound = "warp"
		},
		func(c *Config) { c.Outbounds[0].Type = "warp"; c.Outbounds[0].ProxyAddress = "127.0.0.1:40000" },
		func(c *Config) { c.Outbounds[0].PublicInternet = false },
	} {
		c := Default("state")
		modify(&c)
		if err := Validate(c); err == nil || err.Error() != DirectRequired {
			t.Fatalf("unprotected direct: %v", err)
		}
	}
	c := Default("state")
	c.Outbounds[1].Enabled = true
	c.DefaultOutbound = "warp"
	if err := Validate(c); err != nil {
		t.Fatal("another default route remains allowed", err)
	}
	if _, err := RemoveOutbound(c, "direct", "warp"); err == nil || err.Error() != DirectProtected {
		t.Fatal(err)
	}
}

func TestRemoveOutboundReplacesAllReferencesWithoutMutatingInput(t *testing.T) {
	c := Default("state")
	c.DefaultOutbound = "warp"
	c.Outbounds[1].Enabled = true
	c.Rules = append(c.Rules, Rule{ID: "adaptive", Domains: []string{"example.com"}, Adaptive: true, Candidates: []string{"warp", "direct", "warp"}})
	before, _ := json.Marshal(c)
	next, err := RemoveOutbound(c, "warp", "direct")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(c)
	if string(before) != string(after) {
		t.Fatal("removal mutated input before publication")
	}
	if next.Revision != c.Revision || len(next.Outbounds) != 1 || next.DefaultOutbound != "direct" || len(next.Rules) != len(c.Rules) || next.Rules[1].Outbound != "direct" {
		t.Fatal(next)
	}
	if !reflect.DeepEqual(next.Adaptive.Candidates, []string{"direct"}) || !reflect.DeepEqual(next.Rules[2].Candidates, []string{"direct"}) {
		t.Fatal("candidate references or duplicates remain")
	}
	if !reflect.DeepEqual(next.Rules[1].Domains, c.Rules[1].Domains) || next.Rules[1].ID != c.Rules[1].ID {
		t.Fatal("rule matches lost")
	}
}

func TestRemoveOutboundRequiresExplicitValidReplacement(t *testing.T) {
	c := Default("state")
	c.Outbounds = append(c.Outbounds, Outbound{ID: "company", Type: "tailscale", Enabled: true, StateDir: "state/company"}, Outbound{ID: "disabled", Type: "direct", PublicInternet: true})
	for _, replacement := range []string{"", "missing", "warp", "disabled", "company"} {
		before, _ := json.Marshal(c)
		if _, err := RemoveOutbound(c, "warp", replacement); err == nil {
			t.Fatal("unsafe replacement accepted", replacement)
		}
		after, _ := json.Marshal(c)
		if string(before) != string(after) {
			t.Fatal("rejected change mutated input")
		}
	}
	if _, err := RemoveOutbound(c, "missing", "direct"); err == nil || err.Error() != OutboundMissing {
		t.Fatal(err)
	}
	// A private-only fixed route can be explicitly moved to another company route.
	c.Rules = []Rule{{ID: "company", Domains: []string{"company.example"}, Outbound: "company"}}
	c.Adaptive.Candidates = []string{"direct"}
	c.Outbounds = append(c.Outbounds, Outbound{ID: "company2", Type: "tailscale", Enabled: true, StateDir: "state/company2"})
	next, err := RemoveOutbound(c, "company", "company2")
	if err != nil || next.Rules[0].Outbound != "company2" {
		t.Fatal(next, err)
	}
	// No references: there is no reason to change any route.
	next, err = RemoveOutbound(c, "warp", "")
	if err != nil || !reflect.DeepEqual(c.Rules, next.Rules) || next.DefaultOutbound != c.DefaultOutbound {
		t.Fatal(next, err)
	}
}

func TestRemoveOutboundReplacesGroupReferencesAndDedupes(t *testing.T) {
	for _, kind := range []string{"select", "fallback"} {
		for _, replacement := range []string{"vpn-b", "vpn-c"} {
			t.Run(kind+"/"+replacement, func(t *testing.T) {
				c := phase1GroupConfig(t)
				// Isolate group references; legacy rule/adaptive references must not hide missing group tracking.
				c.Rules = nil
				c.Adaptive.Candidates = []string{"direct"}
				c.Outbounds = append(c.Outbounds, Outbound{ID: "vpn-c", Type: "warp", Enabled: true, PublicInternet: true, ProxyAddress: "127.0.0.1:40002"})
				c.OutboundGroups[0].Type = kind
				if kind == "select" {
					c.OutboundGroups[0].Selected = "warp"
				}
				if err := Validate(c); err != nil {
					t.Fatalf("invalid baseline: %v", err)
				}
				before := phase1JSON(t, c)
				for _, invalid := range []string{"", "missing", "warp", "direct", "group-1"} {
					if _, err := RemoveOutbound(c, "warp", invalid); err == nil {
						t.Fatalf("unsafe replacement %q accepted", invalid)
					}
					if string(phase1JSON(t, c)) != string(before) {
						t.Fatal("rejected removal mutated input")
					}
				}
				next, err := RemoveOutbound(c, "warp", replacement)
				if err != nil {
					t.Fatal(err)
				}
				if string(phase1JSON(t, c)) != string(before) {
					t.Fatal("removal mutated input")
				}
				want := c
				want.Outbounds = append([]Outbound{c.Outbounds[0]}, c.Outbounds[2:]...)
				want.OutboundGroups = append([]OutboundGroup(nil), c.OutboundGroups...)
				want.OutboundGroups[0].Members = []string{replacement, "vpn-b"}
				if replacement == "vpn-b" {
					want.OutboundGroups[0].Members = []string{"vpn-b"}
				}
				if kind == "select" {
					want.OutboundGroups[0].Selected = replacement
				}
				if !reflect.DeepEqual(next, want) {
					t.Fatalf("removal changed unrelated settings: got %+v, want %+v", next, want)
				}
				if err := Validate(next); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func outboundReplacementConfig(t *testing.T) Config {
	t.Helper()
	c := phase1GroupConfig(t)
	c.Revision = 17
	c.Outbounds = append(c.Outbounds, Outbound{ID: "vpn-c", Type: "warp", Enabled: true, PublicInternet: true, ProxyAddress: "127.0.0.1:40002"})
	c.OutboundGroups[0].Members = []string{"vpn-b", "vpn-c"}
	c.Adaptive.Candidates = []string{"direct", "vpn-b"}
	c.Rules[1].Outbound = "vpn-b"
	c.Rules[1].CIDRs = []string{"203.0.113.0/24"}
	c.Rules[1].Sources = []string{"192.0.2.0/24"}
	c.Rules[1].Inbounds = []string{"socks5"}
	c.Rules[1].Ports = []string{"443", "8000-8100"}
	c.Rules[1].Sets = []string{"set-1"}
	c.Rules[1].Family = "ipv6"
	c.Rules = append(c.Rules, Rule{ID: "adaptive-kept", Domains: []string{"adaptive.example"}, Adaptive: true, Candidates: []string{"direct", "vpn-b"}})
	if err := Validate(c); err != nil {
		t.Fatalf("invalid replacement baseline: %v", err)
	}
	return c
}

func TestRemoveOutboundGroupReplacementForFixedRoutes(t *testing.T) {
	for _, group := range []OutboundGroup{
		{ID: "group-1", Type: "select", Members: []string{"vpn-b", "vpn-c"}, Selected: "vpn-c"},
		{ID: "group-1", Type: "fallback", Members: []string{"vpn-b", "vpn-c"}},
		{ID: "group-1", Type: "load-balance", Members: []string{"vpn-b", "vpn-c"}, Strategy: "round-robin"},
		{ID: "group-1", Type: "load-balance", Members: []string{"vpn-b", "vpn-c"}, Strategy: "consistent-hashing", Key: "source"},
	} {
		for _, reference := range []string{"default", "fixed-rule", "both"} {
			for _, enabled := range []bool{true, false} {
				name := group.Type + "/" + group.Strategy + "/" + reference
				if !enabled {
					name += "/disabled"
				}
				t.Run(name, func(t *testing.T) {
					c := outboundReplacementConfig(t)
					c.OutboundGroups[0] = group
					c.OutboundGroups[0].Enabled = enabled
					if reference != "fixed-rule" {
						c.DefaultOutbound = "warp"
					}
					if reference != "default" {
						c.Rules[1].Outbound = "warp"
					}
					if err := Validate(c); err != nil {
						t.Fatalf("invalid input configuration: %v", err)
					}
					before := phase1JSON(t, c)
					next, err := RemoveOutbound(c, "warp", "group-1")
					if string(phase1JSON(t, c)) != string(before) {
						t.Fatal("removal mutated input")
					}
					if !enabled {
						if err == nil {
							t.Fatal("disabled replacement group accepted")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					want := c
					want.Outbounds = slices.Delete(slices.Clone(c.Outbounds), 1, 2)
					want.Rules = slices.Clone(c.Rules)
					if reference != "fixed-rule" {
						want.DefaultOutbound = "group-1"
					}
					if reference != "default" {
						want.Rules[1].Outbound = "group-1"
					}
					if !reflect.DeepEqual(next, want) {
						t.Fatalf("replacement changed unrelated data: got %+v, want %+v", next, want)
					}
					if err := Validate(next); err != nil {
						t.Fatalf("invalid replacement result: %v", err)
					}
				})
			}
		}
	}
}

func TestRemoveOutboundGroupReplacementRejectsOutboundOnlyReferences(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*Config)
	}{
		{"global-adaptive", func(c *Config) { c.Adaptive.Candidates = []string{"warp", "direct"} }},
		{"rule-adaptive", func(c *Config) { c.Rules[2].Candidates = []string{"warp", "vpn-b"} }},
		{"group-member", func(c *Config) {
			c.OutboundGroups = append(c.OutboundGroups, OutboundGroup{ID: "member-ref", Type: "fallback", Enabled: true, Members: []string{"warp", "vpn-b"}})
		}},
		{"group-selected", func(c *Config) {
			// A valid selected reference is also a member reference.
			c.OutboundGroups = append(c.OutboundGroups, OutboundGroup{ID: "selected-ref", Type: "select", Enabled: true, Members: []string{"vpn-b", "warp"}, Selected: "warp"})
		}},
	} {
		for _, fixedRoutes := range []bool{false, true} {
			name := tc.name
			if fixedRoutes {
				name += "/with-fixed-routes"
			}
			t.Run(name, func(t *testing.T) {
				c := outboundReplacementConfig(t)
				tc.modify(&c)
				if fixedRoutes {
					c.DefaultOutbound = "warp"
					c.Rules[1].Outbound = "warp"
				}
				if err := Validate(c); err != nil {
					t.Fatalf("invalid input configuration: %v", err)
				}
				before := phase1JSON(t, c)
				if _, err := RemoveOutbound(c, "warp", "group-1"); err == nil {
					t.Fatal("group accepted for an outbound-only reference")
				}
				if string(phase1JSON(t, c)) != string(before) {
					t.Fatal("rejected removal mutated input")
				}
			})
		}
	}
}

func TestRemoveOutboundReplacementUpdatesCombinedReferences(t *testing.T) {
	for _, replacement := range []string{"vpn-b", "vpn-d"} {
		t.Run(replacement, func(t *testing.T) {
			c := outboundReplacementConfig(t)
			c.Outbounds = append(c.Outbounds, Outbound{ID: "vpn-d", Type: "warp", Enabled: true, PublicInternet: true, ProxyAddress: "127.0.0.1:40003"})
			c.DefaultOutbound = "warp"
			c.Rules[1].Outbound = "warp"
			c.Adaptive.Candidates = []string{"warp", "vpn-b", "direct", "warp"}
			c.Rules[2].Candidates = []string{"vpn-b", "warp", "warp", "direct"}
			c.OutboundGroups = append(
				c.OutboundGroups,
				OutboundGroup{ID: "fallback-ref", Type: "fallback", Enabled: true, Members: []string{"warp", "vpn-b", "vpn-c"}},
				OutboundGroup{ID: "select-ref", Type: "select", Enabled: true, Members: []string{"vpn-b", "warp", "vpn-c"}, Selected: "warp"},
			)
			if err := Validate(c); err != nil {
				t.Fatalf("invalid input configuration: %v", err)
			}
			before := phase1JSON(t, c)
			next, err := RemoveOutbound(c, "warp", replacement)
			if err != nil {
				t.Fatal(err)
			}
			if string(phase1JSON(t, c)) != string(before) {
				t.Fatal("removal mutated input")
			}
			want := c
			want.Outbounds = slices.Delete(slices.Clone(c.Outbounds), 1, 2)
			want.DefaultOutbound = replacement
			want.Rules = slices.Clone(c.Rules)
			want.Rules[1].Outbound = replacement
			want.OutboundGroups = slices.Clone(c.OutboundGroups)
			want.OutboundGroups[2].Selected = replacement
			if replacement == "vpn-b" {
				want.Adaptive.Candidates = []string{"vpn-b", "direct"}
				want.Rules[2].Candidates = []string{"vpn-b", "direct"}
				want.OutboundGroups[1].Members = []string{"vpn-b", "vpn-c"}
				want.OutboundGroups[2].Members = []string{"vpn-b", "vpn-c"}
			} else {
				want.Adaptive.Candidates = []string{"vpn-d", "vpn-b", "direct"}
				want.Rules[2].Candidates = []string{"vpn-b", "vpn-d", "direct"}
				want.OutboundGroups[1].Members = []string{"vpn-d", "vpn-b", "vpn-c"}
				want.OutboundGroups[2].Members = []string{"vpn-b", "vpn-d", "vpn-c"}
			}
			if !reflect.DeepEqual(next, want) {
				t.Fatalf("replacement failed to preserve data or reference order: got %+v, want %+v", next, want)
			}
			if err := Validate(next); err != nil {
				t.Fatalf("invalid replacement result: %v", err)
			}
		})
	}
}
