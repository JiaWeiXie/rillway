package engine

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"rillway/internal/config"
	"rillway/internal/outbound"
	"rillway/internal/routeinfo"
	"testing"
	"time"
)

func successfulRouteProviders(used chan<- string) map[string]outbound.Provider {
	providers := make(map[string]outbound.Provider)
	for _, id := range []string{"direct", "warp", "private"} {
		providers[id] = &testProvider{id: id, dial: func(context.Context, string, string) (net.Conn, error) {
			used <- id
			a, b := net.Pipe()
			_ = b.Close()
			return a, nil
		}}
	}
	return providers
}

func dialRouteWithInfo(t *testing.T, e *Engine, used <-chan string, info routeinfo.Info, address string) string {
	t.Helper()
	conn, err := e.DialContext(routeinfo.With(t.Context(), info), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	select {
	case id := <-used:
		return id
	case <-time.After(time.Second):
		t.Fatal("successful dial did not identify a provider")
		return ""
	}
}

func TestRuleDialUsesSourceInboundAndDestinationPort(t *testing.T) {
	e, _ := testEngine()
	cfg := cloneConfig(e.cfg)
	cfg.Adaptive.Enabled = false
	cfg.Rules = []config.Rule{{ID: "composite", Suffixes: []string{"example.com"}, Sources: []string{"203.0.113.0/24"}, Inbounds: []string{"socks5"}, Ports: []string{"8000-8100"}, Outbound: "warp", Family: "ipv4"}}
	used := make(chan string, 1)
	e.Update(cfg, successfulRouteProviders(used))
	for _, tt := range []struct {
		name, address          string
		info                   routeinfo.Info
		outbound, rule, family string
	}{
		{"all conditions", "a.example.com:8050", routeinfo.Info{SourceIP: netip.MustParseAddr("203.0.113.7"), Inbound: "socks5"}, "warp", "composite", "ipv4"},
		{"mapped source", "A.EXAMPLE.COM.:8050", routeinfo.Info{SourceIP: netip.MustParseAddr("::ffff:203.0.113.7"), Inbound: "socks5"}, "warp", "composite", "ipv4"},
		{"HTTP", "a.example.com:8050", routeinfo.Info{SourceIP: netip.MustParseAddr("203.0.113.7"), Inbound: "http"}, "direct", "default", "auto"},
		{"unknown source", "a.example.com:8050", routeinfo.Info{Inbound: "socks5"}, "direct", "default", "auto"},
		{"unknown inbound", "a.example.com:8050", routeinfo.Info{SourceIP: netip.MustParseAddr("203.0.113.7")}, "direct", "default", "auto"},
		{"wrong port", "a.example.com:8101", routeinfo.Info{SourceIP: netip.MustParseAddr("203.0.113.7"), Inbound: "socks5"}, "direct", "default", "auto"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			host, port, err := net.SplitHostPort(tt.address)
			if err != nil {
				t.Fatal(err)
			}
			r, err := e.selectRoute(target{host: normalize(host), port: port, source: tt.info.SourceIP.Unmap(), inbound: tt.info.Inbound})
			if err != nil || r.family != tt.family {
				t.Fatalf("selected address-family policy = %+v, %v; want %q", r, err, tt.family)
			}
			if got := dialRouteWithInfo(t, e, used, tt.info, tt.address); got != tt.outbound {
				t.Fatalf("outbound = %q, want %q", got, tt.outbound)
			}
			flows := e.Snapshot().Flows
			if len(flows) == 0 || flows[0].Rule != tt.rule || flows[0].Outbound != tt.outbound || flows[0].Family != "unknown" {
				t.Fatalf("latest flow lost selected rule/outbound or invented a destination family: %+v", flows)
			}
		})
	}
}

func TestRuleUpdateRecompilesRuleSetsAndOwnConditions(t *testing.T) {
	e, _ := testEngine()
	cfg := cloneConfig(e.cfg)
	cfg.Adaptive.Enabled = false
	cfg.Rules = []config.Rule{{ID: "set-route", Sources: []string{"203.0.113.0/24"}, Inbounds: []string{"socks5"}, Ports: []string{"443"}, Sets: []string{"s1"}, Outbound: "warp"}}
	cfg.RuleSets = []config.RuleSet{{ID: "s1", Name: "suffixes", Suffixes: []string{"a.example"}}}
	e.Update(cfg, e.providers)
	info := target{host: "x.a.example", port: "443", source: netip.MustParseAddr("203.0.113.7"), inbound: "socks5"}
	assertRule := func(v target, want string) {
		t.Helper()
		r, err := e.selectRoute(v)
		if err != nil || r.rule != want {
			t.Fatalf("route(%+v) = %+v, %v; want rule %q", v, r, err, want)
		}
	}
	assertRule(info, "set-route")
	cfg.RuleSets[0].Suffixes[0] = "b.example"
	cfg.Rules[0].Ports[0] = "8443"
	cfg.Rules[0].Sources[0] = "198.51.100.0/24"
	cfg.Rules[0].Inbounds[0] = "http"
	assertRule(info, "set-route")
	next := target{host: "x.b.example", port: "8443", source: netip.MustParseAddr("198.51.100.7"), inbound: "http"}
	assertRule(next, "default")
	e.Update(cfg, e.providers)
	assertRule(info, "default")
	assertRule(next, "set-route")
}

func TestRuleUpdateOwnsDeepCopyOfAllRoutingCollections(t *testing.T) {
	e, _ := testEngine()
	cfg := cloneConfig(e.cfg)
	cfg.Rules = []config.Rule{{ID: "copy", Domains: []string{"a.example"}, Suffixes: []string{"example"}, CIDRs: []string{"203.0.113.0/24"}, Sources: []string{"192.0.2.0/24"}, Inbounds: []string{"socks5"}, Ports: []string{"443"}, Sets: []string{"s1"}, Adaptive: true, Candidates: []string{"direct", "warp"}, Outbound: "direct"}}
	cfg.OutboundGroups = []config.OutboundGroup{{ID: "group", Type: "fallback", Enabled: true, Members: []string{"warp", "private"}}}
	cfg.RuleSets = []config.RuleSet{{ID: "s1", Name: "set", Domains: []string{"b.example"}, Suffixes: []string{"b.example"}, CIDRs: []string{"198.51.100.0/24"}, Sources: []string{"203.0.113.0/24"}}}
	before, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.Update(cfg, e.providers)
	for _, values := range [][]string{
		cfg.Rules[0].Domains, cfg.Rules[0].Suffixes, cfg.Rules[0].CIDRs, cfg.Rules[0].Sources,
		cfg.Rules[0].Inbounds, cfg.Rules[0].Ports, cfg.Rules[0].Sets, cfg.Rules[0].Candidates,
		cfg.OutboundGroups[0].Members, cfg.RuleSets[0].Domains, cfg.RuleSets[0].Suffixes,
		cfg.RuleSets[0].CIDRs, cfg.RuleSets[0].Sources, cfg.Adaptive.Candidates,
	} {
		values[0] = "changed"
	}
	cfg.Rules[0].ID = "changed-rule"
	cfg.OutboundGroups[0].ID = "changed-group"
	cfg.RuleSets[0].Name = "changed-set"
	after, err := json.Marshal(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("caller mutated engine config after Update\nbefore: %s\nafter: %s", before, after)
	}
}

func TestRuleAdaptivePolicyDoesNotLeakAcrossSourceOrInbound(t *testing.T) {
	for _, dimension := range []string{"source", "inbound"} {
		t.Run(dimension, func(t *testing.T) {
			e, now := testEngine()
			cfg := cloneConfig(e.cfg)
			cfg.Rules = []config.Rule{{ID: "learned", Domains: []string{"public.example"}, Outbound: "direct", Adaptive: true, Candidates: []string{"direct", "warp"}}, {ID: "restricted", Domains: []string{"public.example"}, Outbound: "direct", Adaptive: true, Candidates: []string{"direct"}}}
			info := routeinfo.Info{SourceIP: netip.MustParseAddr("203.0.113.7"), Inbound: "socks5"}
			other := info
			if dimension == "source" {
				cfg.Rules[0].Sources = []string{"203.0.113.0/24"}
				cfg.Rules[1].Sources = []string{"198.51.100.0/24"}
				other.SourceIP = netip.MustParseAddr("198.51.100.7")
			} else {
				cfg.Rules[0].Inbounds = []string{"socks5"}
				cfg.Rules[1].Inbounds = []string{"http"}
				other.Inbound = "http"
			}
			used := make(chan string, 1)
			providers := successfulRouteProviders(used)
			e.Update(cfg, providers)
			const address = "public.example:443"
			if got := dialRouteWithInfo(t, e, used, info, address); got != "direct" {
				t.Fatalf("initial route = %q", got)
			}
			*now = now.Add(11 * time.Minute)
			e.mu.Lock()
			var learned *adaptiveState
			for _, d := range e.destinations {
				if d.rule == "learned" {
					learned = d
					break
				}
			}
			if learned != nil {
				for range 3 {
					e.recordLocked(learned, "direct", 200*time.Millisecond, nil)
				}
				for range 4 {
					e.recordLocked(learned, "warp", 20*time.Millisecond, nil)
				}
			}
			e.mu.Unlock()
			if learned == nil {
				t.Fatal("matching adaptive rule did not create observations")
			}
			if got := dialRouteWithInfo(t, e, used, info, address); got != "warp" {
				t.Fatalf("learned route = %q, want warp", got)
			}
			// An unrelated update must re-evaluate the saved source/inbound,
			// rather than testing this rule with unknown metadata and resetting it.
			next := cloneConfig(cfg)
			next.Rules = append(next.Rules, config.Rule{ID: "unrelated", Domains: []string{"other.example"}, Outbound: "direct"})
			e.Update(next, providers)
			if got := dialRouteWithInfo(t, e, used, info, address); got != "warp" {
				t.Fatalf("unrelated update lost learned metadata route: %q", got)
			}
			if got := dialRouteWithInfo(t, e, used, other, address); got != "direct" {
				t.Fatalf("learned route leaked to different %s: %q", dimension, got)
			}
			flows := e.Snapshot().Flows
			if len(flows) == 0 || flows[0].Rule != "restricted" || flows[0].Outbound != "direct" {
				t.Fatalf("restricted flow uses another request's policy: %+v", flows)
			}
		})
	}
}
