package config

import (
	"encoding/json"
	"reflect"
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
