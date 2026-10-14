package engine

import (
	"context"
	"net"
	"net/netip"
	"rillway/internal/config"
	"rillway/internal/outbound"
	"rillway/internal/routeinfo"
	"testing"
	"time"
)

func TestAdaptiveAffinitySeparatesRootAndSubdomainPolicies(t *testing.T) {
	for _, ruleID := range []string{"api-policy", "default"} {
		t.Run(ruleID, func(t *testing.T) {
			e, _ := testEngine()
			e.cfg.Rules = []config.Rule{{ID: ruleID, Suffixes: []string{"api.example.com"}, Adaptive: true, Outbound: "direct", Candidates: []string{"direct", "warp"}}}
			last := ""
			provider := func(id string) outbound.Provider {
				return testProvider{id: id, dial: func(context.Context, string, string) (net.Conn, error) {
					last = id
					a, b := net.Pipe()
					_ = b.Close()
					return a, nil
				}}
			}
			e.Update(e.cfg, map[string]outbound.Provider{"direct": provider("direct"), "warp": provider("warp")})
			dial := func(host, want string) {
				t.Helper()
				conn, err := e.DialContext(t.Context(), "tcp", host+":443")
				if err != nil {
					t.Fatal(err)
				}
				_ = conn.Close()
				if last != want {
					t.Errorf("%s used %s; want %s", host, last, want)
				}
			}
			dial("api.example.com", "direct")
			// Seed a learned choice, then interleave the conflicting root policy.
			for _, state := range e.destinations {
				state.current = "warp"
			}
			dial("example.com", "direct")
			dial("ws.api.example.com", "warp")
			dial("cdn.example.com", "direct")
			dial("api.example.com", "warp")
			if got := len(e.Snapshot().Destinations); got != 2 {
				t.Errorf("got %d policy states, want two shared groups", got)
			}
		})
	}
}

func TestAdaptiveSharedProbeHonorsUpdatedFixedIngressRule(t *testing.T) {
	e, now := testEngine()
	provider := func(id string) outbound.Provider {
		return &testProvider{id: id, dial: func(context.Context, string, string) (net.Conn, error) {
			a, b := net.Pipe()
			_ = b.Close()
			return a, nil
		}}
	}
	e.Update(e.cfg, map[string]outbound.Provider{"direct": provider("direct"), "warp": provider("warp")})
	contexts := []context.Context{t.Context(), routeinfo.With(t.Context(), routeinfo.Info{SourceIP: netip.MustParseAddr("198.51.100.9"), Inbound: "socks5"})}
	for i, host := range []string{"a.example.com:443", "b.example.com:443"} {
		conn, err := e.DialContext(contexts[i], "tcp", host)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	}
	next := cloneConfig(e.cfg)
	next.Rules = []config.Rule{{ID: "fixed-b", Domains: []string{"b.example.com"}, Sources: []string{"198.51.100.9/32"}, Inbounds: []string{"socks5"}, Outbound: "warp"}}
	e.Update(next, e.providers)
	*now = now.Add(2 * time.Minute)
	for _, job := range e.scheduleProbes() {
		if job.address == "b.example.com:443" {
			t.Errorf("fixed ingress destination scheduled on old adaptive candidate %s", job.id)
		}
	}
}

func TestAdaptiveSharedStateSurvivesResolvedSelfAlias(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		name := "internal"
		if blocked {
			name = "proxy-loop"
		}
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			e, now := testEngine()
			provider := func(id string) outbound.Provider {
				return testProvider{id: id, dial: func(ctx context.Context, network, address string) (net.Conn, error) {
					if address == "self.example.com:443" {
						for _, job := range e.scheduleProbes() {
							if job.address == address {
								t.Error("unresolved self alias published as shared probe target")
							}
						}
						return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
					}
					a, b := net.Pipe()
					_ = b.Close()
					return a, nil
				}}
			}
			e.Update(e.cfg, map[string]outbound.Provider{"direct": provider("direct"), "warp": provider("warp")})
			e.SetDestinationPolicy(func(address string) (bool, bool) {
				self := address == listener.Addr().String()
				return self && blocked, self && !blocked
			})
			conn, err := e.DialContext(t.Context(), "tcp", "www.example.com:443")
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.Close()
			for _, state := range e.destinations {
				state.current = "warp"
			}
			*now = now.Add(2 * time.Minute)
			conn, err = e.DialContext(t.Context(), "tcp", "self.example.com:443")
			if blocked && err == nil || !blocked && err != nil {
				t.Errorf("self alias: blocked=%v error=%v", blocked, err)
			}
			if conn != nil {
				_ = conn.Close()
			}
			peer, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			_ = peer.Close()
			snapshot := e.Snapshot()
			if len(snapshot.Destinations) != 1 || snapshot.Destinations[0].Outbound != "warp" || len(snapshot.Flows) != 1 {
				t.Fatalf("self alias disturbed learned public state: %+v", snapshot)
			}
		})
	}
}
