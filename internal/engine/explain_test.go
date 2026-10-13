package engine

import (
	"context"
	"net"
	"reflect"
	"rillway/internal/config"
	"rillway/internal/outbound"
	"slices"
	"testing"
)

func TestExplainRejectsInvalidInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*config.ExplainRequest)
	}{
		{"empty host", func(r *config.ExplainRequest) { r.Host = "" }},
		{"blank host", func(r *config.ExplainRequest) { r.Host = " \t " }},
		{"empty port", func(r *config.ExplainRequest) { r.Port = "" }},
		{"non-numeric port", func(r *config.ExplainRequest) { r.Port = "https" }},
		{"zero port", func(r *config.ExplainRequest) { r.Port = "0" }},
		{"negative port", func(r *config.ExplainRequest) { r.Port = "-1" }},
		{"overflow port", func(r *config.ExplainRequest) { r.Port = "65536" }},
		{"range port", func(r *config.ExplainRequest) { r.Port = "80-443" }},
		{"invalid source", func(r *config.ExplainRequest) { r.Source = "not-an-ip" }},
		{"source prefix", func(r *config.ExplainRequest) { r.Source = "192.0.2.1/32" }},
		{"invalid inbound", func(r *config.ExplainRequest) { r.Inbound = "ftp" }},
		{"invalid family", func(r *config.ExplainRequest) { r.Family = "tcp4" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := testEngine()
			r := config.ExplainRequest{Host: "example.com", Port: "443"}
			tc.change(&r)
			if result, err := e.Explain(r); err == nil {
				t.Fatalf("invalid request accepted: %+v -> %+v", r, result)
			}
		})
	}
}

func TestExplainFixedDefaultAndAdaptiveRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, host, rule, kind, target, resolved, reason, family string
		adaptive                                                 bool
		rules                                                    []config.Rule
	}{
		{name: "default", host: "other.example", rule: "default", kind: "default", target: "direct", resolved: "direct", reason: "default", family: "auto"},
		{name: "adaptive default", host: "other.example", rule: "default", kind: "default", target: "direct", resolved: "direct", reason: "adaptive_initial", family: "auto", adaptive: true},
		{name: "disabled adaptive rule", host: "api.example.com", rule: "adaptive", kind: "outbound", target: "direct", resolved: "direct", reason: "adaptive_initial", family: "auto", rules: []config.Rule{{ID: "adaptive", Suffixes: []string{"example.com"}, Outbound: "direct", Adaptive: true, Candidates: []string{"direct", "warp"}}}},
		{name: "rule named default", host: "api.example.com", rule: "default", kind: "outbound", target: "warp", resolved: "warp", reason: "fixed_rule", family: "auto", rules: []config.Rule{{ID: "default", Domains: []string{"api.example.com"}, Outbound: "warp"}}},
		{name: "fixed", host: "API.EXAMPLE.COM.", rule: "fixed", kind: "outbound", target: "warp", resolved: "warp", reason: "fixed_rule", family: "ipv6", rules: []config.Rule{{ID: "fixed", Domains: []string{"api.example.com"}, Outbound: "warp", Family: "ipv6"}}},
		{name: "adaptive rule", host: "api.example.com", rule: "adaptive", kind: "outbound", target: "direct", resolved: "direct", reason: "adaptive_initial", family: "auto", adaptive: true, rules: []config.Rule{{ID: "adaptive", Suffixes: []string{"example.com"}, Outbound: "direct", Adaptive: true, Candidates: []string{"direct", "warp"}}}},
		{name: "fixed precedes adaptive", host: "api.example.com", rule: "fixed", kind: "outbound", target: "warp", resolved: "warp", reason: "fixed_rule", family: "auto", adaptive: true, rules: []config.Rule{{ID: "adaptive", Suffixes: []string{"example.com"}, Adaptive: true, Candidates: []string{"direct", "warp"}}, {ID: "fixed", Domains: []string{"api.example.com"}, Outbound: "warp"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := testEngine()
			c := cloneConfig(e.cfg)
			c.Adaptive.Enabled = tc.adaptive
			c.Rules = tc.rules
			e.Update(c, e.providers)
			got, err := e.Explain(config.ExplainRequest{Host: tc.host, Port: "443"})
			if err != nil || got.Blocked || got.Private || got.RuleID != tc.rule || got.TargetKind != tc.kind || got.TargetID != tc.target || got.ResolvedID != tc.resolved || got.Reason != tc.reason || got.Family != tc.family {
				t.Fatalf("result = %+v, error = %v", got, err)
			}
		})
	}
}

func TestExplainUsesCompositeMetadataAndDirectAlias(t *testing.T) {
	e, _ := testEngine()
	c := cloneConfig(e.cfg)
	c.Adaptive.Enabled = false
	c.Outbounds = append(c.Outbounds, config.Outbound{ID: "direct-extra", Type: "direct", Enabled: true, PublicInternet: true})
	c.Rules = []config.Rule{{ID: "source-socks", Domains: []string{"example.com"}, Sources: []string{"192.0.2.0/24"}, Inbounds: []string{"socks5"}, Ports: []string{"8000-8100"}, Outbound: "direct-extra"}}
	providers := make(map[string]outbound.Provider)
	for id, p := range e.providers {
		providers[id] = p
	}
	providers["direct-extra"] = testProvider{id: "direct-extra"}
	e.Update(c, providers)
	for _, tc := range []struct{ source, inbound, port, rule, target string }{
		{"::ffff:192.0.2.7", "socks5", "8050", "source-socks", "direct-extra"},
		{"192.0.2.7", "http", "8050", "default", "direct"},
		{"", "socks5", "8050", "default", "direct"},
		{"192.0.2.7", "", "8050", "default", "direct"},
		{"192.0.2.7", "socks5", "443", "default", "direct"},
	} {
		got, err := e.Explain(config.ExplainRequest{Host: "EXAMPLE.COM.", Port: tc.port, Source: tc.source, Inbound: tc.inbound})
		if err != nil || got.Blocked || got.RuleID != tc.rule || got.TargetID != tc.target || got.ResolvedID != tc.target {
			t.Fatalf("%+v -> %+v, %v", tc, got, err)
		}
	}
	c.Rules = []config.Rule{{ID: "alias-private", Domains: []string{"nas"}, Outbound: "direct-extra"}}
	e.Update(c, providers)
	got, err := e.Explain(config.ExplainRequest{Host: "nas", Port: "1"})
	if err != nil || !got.Private || got.Blocked || got.ResolvedID != "direct-extra" || got.Reason != "fixed_rule" {
		t.Fatalf("direct alias private route = %+v, %v", got, err)
	}
}

func TestExplainGroupReasonsAndOrders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		group  config.OutboundGroup
		reason string
		order  []string
	}{
		{"select", config.OutboundGroup{Type: "select", Selected: "B"}, "group_select", []string{"B"}},
		{"fallback", config.OutboundGroup{Type: "fallback"}, "group_fallback", []string{"A", "B"}},
		{"round robin", config.OutboundGroup{Type: "load-balance", Strategy: "round-robin"}, "group_round_robin", []string{"A", "B"}},
		{"destination hash", config.OutboundGroup{Type: "load-balance", Strategy: "consistent-hashing", Key: "destination"}, "group_consistent_hashing", nil},
		{"source hash", config.OutboundGroup{Type: "load-balance", Strategy: "consistent-hashing", Key: "source"}, "group_consistent_hashing", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newGroupHarness(tc.group)
			h.cfg.Rules = []config.Rule{{ID: "group-rule", Domains: []string{"example.com"}, Outbound: "group"}}
			h.e.Update(h.cfg, h.providers)
			got, err := h.e.Explain(config.ExplainRequest{Host: "example.com", Port: "443", Source: "192.0.2.10"})
			if err != nil || got.Blocked || got.Private || got.RuleID != "group-rule" || got.TargetKind != "group" || got.TargetID != "group" || got.Reason != tc.reason || len(got.Order) == 0 || got.ResolvedID != got.Order[0] {
				t.Fatalf("result = %+v, error = %v", got, err)
			}
			if tc.order != nil && !slices.Equal(got.Order, tc.order) {
				t.Fatalf("order = %v, want %v", got.Order, tc.order)
			}
			if tc.order == nil {
				members := slices.Clone(got.Order)
				slices.Sort(members)
				if !slices.Equal(members, []string{"A", "B"}) {
					t.Fatalf("hash order = %v", got.Order)
				}
			}
			if len(h.attempts) != 0 {
				t.Fatalf("preview dialed: %v", h.attempts)
			}
		})
	}
}

func TestExplainBlockedRoutesReturnResults(t *testing.T) {
	for _, tc := range []struct {
		name, host, reason string
		change             func(*groupHarness)
	}{
		{"private group", "nas", "private_protected", func(h *groupHarness) {}},
		{"no providers", "example.com", "no_eligible_member", func(h *groupHarness) { delete(h.providers, "A"); delete(h.providers, "B") }},
		{"disabled group", "example.com", "target_unavailable", func(h *groupHarness) { h.cfg.OutboundGroups[0].Enabled = false }},
		{"selected unavailable", "example.com", "selected_unavailable", func(h *groupHarness) {
			h.cfg.OutboundGroups[0].Type = "select"
			h.cfg.OutboundGroups[0].Selected = "A"
			delete(h.providers, "A")
		}},
		{"private selected", "nas", "selected_unavailable", func(h *groupHarness) {
			h.cfg.OutboundGroups[0].Type = "select"
			h.cfg.OutboundGroups[0].Selected = "A"
			h.cfg.Outbounds[2].Type = "tailscale"
		}},
		{"missing fixed provider", "example.com", "target_unavailable", func(h *groupHarness) {
			h.cfg.Rules = []config.Rule{{ID: "missing", Domains: []string{"example.com"}, Outbound: "A"}}
			delete(h.providers, "A")
		}},
		{"private fixed warp", "nas", "private_protected", func(h *groupHarness) {
			h.cfg.Rules = []config.Rule{{ID: "private", Domains: []string{"nas"}, Outbound: "A"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newGroupHarness(config.OutboundGroup{Type: "fallback"})
			tc.change(h)
			h.e.Update(h.cfg, h.providers)
			got, err := h.e.Explain(config.ExplainRequest{Host: tc.host, Port: "443"})
			if err != nil || !got.Blocked || got.Reason != tc.reason || got.Private != (tc.host == "nas") || got.ResolvedID != "" {
				t.Fatalf("blocked result = %+v, error = %v", got, err)
			}
			if len(h.attempts) != 0 {
				t.Fatal("blocked preview dialed")
			}
		})
	}
}

func TestExplainRoundRobinDoesNotAdvanceOrRecordObservations(t *testing.T) {
	h := newGroupHarness(config.OutboundGroup{Type: "load-balance", Strategy: "round-robin"})
	before := h.e.Snapshot()
	for range 10 {
		got, err := h.e.Explain(config.ExplainRequest{Host: "example.com", Port: "443"})
		if err != nil || !slices.Equal(got.Order, []string{"A", "B"}) {
			t.Fatalf("initial preview = %+v, %v", got, err)
		}
	}
	if !reflect.DeepEqual(before, h.e.Snapshot()) || len(h.e.destinations) != 0 || len(h.e.health) != 0 || len(h.attempts) != 0 {
		t.Fatal("preview changed observations, health, adaptive state or connected")
	}
	if id := h.dial(t, t.Context(), "example.com:443"); id != "A" {
		t.Fatalf("first actual dial = %s", id)
	}
	for range 10 {
		got, err := h.e.Explain(config.ExplainRequest{Host: "example.com", Port: "443"})
		if err != nil || !slices.Equal(got.Order, []string{"B", "A"}) {
			t.Fatalf("preview after dial = %+v, %v", got, err)
		}
	}
	if id := h.dial(t, t.Context(), "example.com:443"); id != "B" {
		t.Fatalf("second actual dial = %s", id)
	}
}

func TestExplainAdaptiveDoesNotDialOrCreateProbeState(t *testing.T) {
	e, _ := testEngine()
	c := cloneConfig(e.cfg)
	c.Rules = []config.Rule{{ID: "adaptive", Domains: []string{"example.com"}, Adaptive: true, Candidates: []string{"direct", "warp"}}}
	dials := 0
	providers := map[string]outbound.Provider{}
	for _, id := range []string{"direct", "warp"} {
		providers[id] = &testProvider{id: id, dial: func(context.Context, string, string) (net.Conn, error) { dials++; return nil, context.Canceled }}
	}
	e.Update(c, providers)
	before := e.Snapshot()
	for range 5 {
		if got, err := e.Explain(config.ExplainRequest{Host: "example.com", Port: "65535", Family: "auto"}); err != nil || got.Blocked || got.Reason != "adaptive_initial" {
			t.Fatalf("adaptive preview = %+v, %v", got, err)
		}
	}
	if dials != 0 || len(e.destinations) != 0 || len(e.health) != 0 || !reflect.DeepEqual(before, e.Snapshot()) || len(e.scheduleProbes()) != 0 {
		t.Fatal("adaptive preview changed routing evidence or scheduled a probe")
	}
}

func TestExplainFamilyAndLiteralHostInputs(t *testing.T) {
	for _, tc := range []struct {
		name, host, ruleFamily, requestFamily, wantFamily string
	}{
		{"default family", "example.com", "", "", "auto"},
		{"explicit auto", "example.com", "auto", "auto", "auto"},
		{"requested ipv4", "example.com", "auto", "ipv4", "ipv4"},
		{"requested ipv6", "example.com", "", "ipv6", "ipv6"},
		{"rule ipv4 wins", "example.com", "ipv4", "ipv6", "ipv4"},
		{"rule ipv6 wins", "example.com", "ipv6", "ipv4", "ipv6"},
		{"IPv4 literal", "203.0.113.7", "", "ipv4", "ipv4"},
		{"IPv6 literal", "2001:db8::7", "", "ipv6", "ipv6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := testEngine()
			c := cloneConfig(e.cfg)
			c.Adaptive.Enabled = false
			c.Rules = []config.Rule{{ID: "family", Ports: []string{"1"}, Outbound: "direct", Family: tc.ruleFamily}}
			e.Update(c, e.providers)
			got, err := e.Explain(config.ExplainRequest{Host: tc.host, Port: "1", Family: tc.requestFamily})
			if err != nil || got.Blocked || got.Family != tc.wantFamily || got.RuleID != "family" {
				t.Fatalf("result = %+v, error = %v; want family %s", got, err, tc.wantFamily)
			}
		})
	}
}

func TestExplainAdaptiveReportsConfiguredInitialWithoutChangingLearnedState(t *testing.T) {
	e, _ := testEngine()
	c := cloneConfig(e.cfg)
	c.Rules = []config.Rule{{ID: "adaptive", Domains: []string{"example.com"}, Outbound: "direct", Adaptive: true, Candidates: []string{"direct", "warp"}}}
	e.Update(c, e.providers)
	e.mu.Lock()
	r, err := e.selectRoute(target{host: "example.com", port: "443"})
	if err != nil {
		e.mu.Unlock()
		t.Fatal(err)
	}
	state := e.destinationLocked("example.com:443|tcp", "example.com:443", "tcp", r)
	state.current = "warp"
	state.reason = "learned choice"
	e.mu.Unlock()
	before := e.Snapshot()
	got, err := e.Explain(config.ExplainRequest{Host: "example.com", Port: "443"})
	if err != nil || got.ResolvedID != "direct" || got.TargetID != "direct" || got.Reason != "adaptive_initial" || !reflect.DeepEqual(before, e.Snapshot()) {
		t.Fatalf("preview = %+v, error = %v; learned state changed or leaked into preview", got, err)
	}
}
