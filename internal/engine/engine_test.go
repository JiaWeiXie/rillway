package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"rillway/internal/config"
	"rillway/internal/outbound"
	"rillway/internal/routeinfo"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type testProvider struct {
	id   string
	dial func(context.Context, string, string) (net.Conn, error)
}

func (p testProvider) ID() string { return p.id }
func (p testProvider) DialContext(c context.Context, n, a string) (net.Conn, error) {
	if p.dial != nil {
		return p.dial(c, n, a)
	}
	return nil, errors.New("test dial failed")
}
func (p testProvider) Status(context.Context) outbound.Status { return outbound.Status{ID: p.id} }
func (p testProvider) Close() error                           { return nil }

func testEngine() (*Engine, *time.Time) {
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	e := &Engine{now: func() time.Time { return now }, ctx: context.Background(), flows: make(map[uint64]*flowState)}
	cfg := config.Config{DefaultOutbound: "direct", Outbounds: []config.Outbound{{ID: "direct", Type: "direct", Enabled: true}, {ID: "warp", Type: "warp", Enabled: true}, {ID: "private", Type: "tailscale", Enabled: true}}, Adaptive: config.Adaptive{Enabled: true, Candidates: []string{"direct", "warp"}}, PAC: config.PAC{BypassDomains: []config.PACBypass{{Value: "company.example", Enabled: true}}}}
	e.Update(cfg, map[string]outbound.Provider{"direct": testProvider{id: "direct"}, "warp": testProvider{id: "warp"}, "private": testProvider{id: "private"}})
	return e, &now
}

func TestRulesFixedPrecedenceAndDomainBoundary(t *testing.T) {
	e, _ := testEngine()
	e.cfg.Rules = []config.Rule{{ID: "auto", Suffixes: []string{"example.com"}, Adaptive: true, Candidates: []string{"direct", "warp"}}, {ID: "fixed", Domains: []string{"api.example.com"}, Outbound: "warp"}, {ID: "suffix", Suffixes: []string{"githubassets.com"}, Outbound: "warp"}, {ID: "cidr", CIDRs: []string{"203.0.113.0/24"}, Outbound: "warp", Family: "ipv4"}}
	e.Update(e.cfg, e.providers)
	for _, tt := range []struct{ host, rule string }{{"api.example.com", "fixed"}, {"cdn.githubassets.com", "suffix"}, {"githubassets.com", "suffix"}, {"evilgithubassets.com", "default"}, {"203.0.113.10", "cidr"}, {"other.example.com", "auto"}} {
		t.Run(tt.host, func(t *testing.T) {
			r, err := e.selectRoute(target{host: tt.host})
			if err != nil || r.rule != tt.rule {
				t.Fatalf("route=%+v err=%v", r, err)
			}
		})
	}
}

func TestPrivateDestinationsNeverUsePublicOutbound(t *testing.T) {
	e, _ := testEngine()
	e.cfg.DefaultOutbound = "warp"
	for _, host := range []string{"service.company.example", "nas", "box.local", "100.100.100.100", "127.0.0.1", "10.0.0.2", "::1", "fd00::1"} {
		if _, err := e.selectRoute(target{host: host}); err == nil {
			t.Errorf("private host allowed: %s", host)
		}
	}
	e.cfg.Rules = []config.Rule{{ID: "company", Suffixes: []string{"company.example"}, Outbound: "private"}}
	e.Update(e.cfg, e.providers)
	r, err := e.selectRoute(target{host: "service.company.example"})
	if err != nil || r.outbound != "private" || len(r.candidates) != 0 {
		t.Fatalf("private route: %+v %v", r, err)
	}
}

func TestFixedWireGuardCanServePrivateNetwork(t *testing.T) {
	e, _ := testEngine()
	e.cfg.Outbounds = append(e.cfg.Outbounds, config.Outbound{ID: "company-vpn", Type: "wireguard", Enabled: true, PublicInternet: true})
	e.providers["company-vpn"] = testProvider{id: "company-vpn"}
	e.cfg.Rules = []config.Rule{{ID: "company", Suffixes: []string{"company.example"}, Outbound: "company-vpn"}}
	e.Update(e.cfg, e.providers)
	r, err := e.selectRoute(target{host: "service.company.example"})
	if err != nil || r.outbound != "company-vpn" || len(r.candidates) != 0 {
		t.Fatalf("fixed private WireGuard route: %+v %v", r, err)
	}
}

func TestPublicWireGuardDefaultCannotReceivePrivateNames(t *testing.T) {
	e, _ := testEngine()
	e.cfg.Outbounds = append(e.cfg.Outbounds, config.Outbound{ID: "public-vpn", Type: "wireguard", Enabled: true, PublicInternet: true})
	e.providers["public-vpn"] = testProvider{id: "public-vpn"}
	e.cfg.DefaultOutbound = "public-vpn"
	for _, host := range []string{"nas", "service.company.example", "100.100.100.100", "10.0.0.1"} {
		if _, err := e.selectRoute(target{host: host}); err == nil {
			t.Errorf("private destination sent to default public WireGuard: %s", host)
		}
	}
	e.cfg.Adaptive.Enabled = false
	e.cfg.Rules = []config.Rule{{ID: "adaptive-private", Suffixes: []string{"company.example"}, Outbound: "public-vpn", Adaptive: true, Candidates: []string{"direct", "public-vpn"}}}
	e.Update(e.cfg, e.providers)
	if _, err := e.selectRoute(target{host: "service.company.example"}); err == nil {
		t.Fatal("disabled adaptive rule treated as explicit fixed private route")
	}
}

func TestUnavailableFixedOutboundDoesNotFallback(t *testing.T) {
	e, _ := testEngine()
	e.cfg.Rules = []config.Rule{{ID: "missing", Domains: []string{"github.com"}, Outbound: "missing"}}
	e.Update(e.cfg, e.providers)
	_, err := e.DialContext(context.Background(), "tcp", "github.com:443")
	if err == nil || !strings.Contains(err.Error(), "no fallback") {
		t.Fatalf("expected no fallback error, got %v", err)
	}
}

func TestAdaptiveCooldownAndTwoObservations(t *testing.T) {
	e, now := testEngine()
	d := e.destinationLocked("x", "public.example:443", "tcp", route{outbound: "direct", candidates: []string{"direct", "warp"}})
	for range 3 {
		e.recordLocked(d, "direct", 200*time.Millisecond, nil)
	}
	for range 3 {
		e.recordLocked(d, "warp", 50*time.Millisecond, nil)
	}
	if d.current != "direct" {
		t.Fatal("switched inside cooldown")
	}
	*now = now.Add(10 * time.Minute)
	// Refresh the baseline and make two qualified observations after cooldown.
	e.recordLocked(d, "direct", 200*time.Millisecond, nil)
	e.recordLocked(d, "direct", 200*time.Millisecond, nil)
	if d.current != "direct" {
		t.Fatal("reused the same candidate evidence across two rounds")
	}
	e.recordLocked(d, "warp", 50*time.Millisecond, nil)
	if d.current != "warp" {
		t.Fatalf("did not switch: %+v", d)
	}
}

func TestAdaptiveFailureRequiresRecentSuccessfulAlternative(t *testing.T) {
	e, now := testEngine()
	d := e.destinationLocked("x", "public.example:443", "tcp", route{outbound: "direct", candidates: []string{"direct", "warp"}})
	e.recordLocked(d, "warp", 100*time.Millisecond, nil)
	e.recordLocked(d, "direct", 0, context.Canceled)
	e.recordLocked(d, "direct", 0, syscall.ECONNREFUSED)
	e.recordLocked(d, "direct", 0, syscall.ENETUNREACH)
	if d.current != "direct" {
		t.Fatal("cancellation counted as network failure")
	}
	e.recordLocked(d, "direct", 0, context.DeadlineExceeded)
	if d.current != "warp" {
		t.Fatal("expected recent healthy alternative")
	}
	*now = now.Add(2 * time.Minute)
	for range 3 {
		e.recordLocked(d, "warp", 0, context.DeadlineExceeded)
	}
	if d.current != "warp" {
		t.Fatal("must not switch to stale/failing direct")
	}
}

func TestProbeBudgetActivityAndPrivateExclusions(t *testing.T) {
	e, now := testEngine()
	for i, host := range []string{"one.example:443", "two.example:80", "three.example:443", "nas:443", "service.company.example:443", "four.example:8443"} {
		key := string(rune('a' + i))
		e.destinationLocked(key, host, "tcp", route{rule: "default", family: "auto", outbound: "direct", candidates: []string{"direct", "warp"}})
	}
	jobs := e.scheduleProbes()
	if len(jobs) != 2 {
		t.Fatalf("concurrency cap: %d", len(jobs))
	}
	if len(e.scheduleProbes()) != 0 {
		t.Fatal("exceeded outstanding probe cap")
	}
	e.probes = 0
	jobs = e.scheduleProbes()
	if len(jobs) != 1 {
		t.Fatalf("expected remaining public destination, got %d", len(jobs))
	}
	e.probes = 0
	if len(e.scheduleProbes()) != 0 {
		t.Fatal("per-destination one-minute budget exceeded")
	}
	e.probeTimes = make([]time.Time, 12)
	for i := range e.probeTimes {
		e.probeTimes[i] = *now
	}
	for _, d := range e.destinations {
		d.lastProbe = time.Time{}
	}
	if len(e.scheduleProbes()) != 0 {
		t.Fatal("global budget exceeded")
	}
	*now = now.Add(6 * time.Minute)
	if len(e.scheduleProbes()) != 0 {
		t.Fatal("inactive destinations must not be probed")
	}
}

func TestTrafficRatesCapacityAndExpiration(t *testing.T) {
	e, now := testEngine()
	f := &flowState{flow: Flow{ID: 1, Started: *now}, now: e.now}
	f.add(100, true)
	f.add(50, false)
	e.flows[1] = f
	s := e.Snapshot()
	if s.Flows[0].UploadRate != 20 || s.Flows[0].DownloadBytes != 50 {
		t.Fatalf("bad counters: %+v", s.Flows[0])
	}
	*now = now.Add(5 * time.Second)
	if e.Snapshot().Flows[0].UploadRate != 0 {
		t.Fatal("idle flow retained rate")
	}
	*now = now.Add(24 * time.Hour)
	if len(e.Snapshot().Flows) != 0 {
		t.Fatal("expired metadata retained")
	}
	for i := range maxFlows {
		e.flows[uint64(i)] = &flowState{flow: Flow{ID: uint64(i), Started: *now}, now: e.now}
	}
	e.pruneFlowsLocked()
	if len(e.flows) != maxFlows-1 {
		t.Fatal("capacity not bounded")
	}
}

func TestEstablishedStreamSurvivesUpdateAndTracksPayload(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		c, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.Copy(c, c)
	}()
	e, _ := testEngine()
	e.providers["direct"] = testProvider{id: "direct", dial: (&net.Dialer{}).DialContext}
	c, err := e.DialContext(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	cfg := e.cfg
	cfg.DefaultOutbound = "warp"
	e.Update(cfg, map[string]outbound.Provider{"warp": testProvider{id: "warp"}})
	if _, err = c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 5)
	if _, err = io.ReadFull(c, buffer); err != nil || string(buffer) != "hello" {
		t.Fatalf("existing connection interrupted: %q %v", buffer, err)
	}
	s := e.Snapshot()
	if len(s.Flows) != 1 || s.Flows[0].Outbound != "direct" || s.Flows[0].UploadBytes != 5 || s.Flows[0].DownloadBytes != 5 {
		t.Fatalf("bad flow %+v", s)
	}
}

func TestConcurrentStatsAndPolicyUpdates(t *testing.T) {
	e, _ := testEngine()
	cfg := e.cfg
	providers := e.providers
	f := &flowState{flow: Flow{Started: e.now()}, now: e.now}
	e.flows[1] = f
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			e.Update(cfg, providers)
		}
	}()
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				f.add(1, true)
				_ = e.Snapshot()
			}
		}()
	}
	wg.Wait()
	if e.Snapshot().Flows[0].UploadBytes != 400 {
		t.Fatal("lost concurrent bytes")
	}
}

// Applying an unrelated configuration change must not move a destination's
// new connections back to the default outbound and change its egress address.
func TestUpdateKeepsLearnedRouteOnlyForUnchangedAdaptivePolicy(t *testing.T) {
	const host = "public.example:443"
	used := make(chan string, 1)
	provider := func(id string) outbound.Provider {
		return &testProvider{id: id, dial: func(context.Context, string, string) (net.Conn, error) {
			used <- id
			a, b := net.Pipe()
			_ = b.Close()
			return a, nil
		}}
	}
	route := func(e *Engine) string {
		t.Helper()
		conn, err := e.DialContext(t.Context(), "tcp", host)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		return <-used
	}
	learn := func(e *Engine, id string) {
		t.Helper()
		if got := route(e); got != "direct" {
			t.Fatalf("initial route %q", got)
		}
		e.mu.Lock()
		for _, state := range e.destinations {
			state.current = id
		}
		e.mu.Unlock()
		if got := route(e); got != id {
			t.Fatalf("learned route %q", got)
		}
	}
	adaptiveRule := func(c *config.Config) {
		c.Rules = []config.Rule{{ID: "auto", Domains: []string{"public.example"}, Outbound: "direct", Adaptive: true, Candidates: []string{"direct", "warp"}}}
	}
	for _, tt := range []struct {
		name    string
		learned string
		change  func(*config.Config, map[string]outbound.Provider)
		want    string
		base    func(*config.Config)
	}{
		{"unrelated rule", "warp", func(c *config.Config, _ map[string]outbound.Provider) {
			c.Rules = append(c.Rules, config.Rule{ID: "other", Domains: []string{"other.example"}, Outbound: "warp"})
		}, "warp", nil},
		{"fixed rule for destination", "warp", func(c *config.Config, _ map[string]outbound.Provider) {
			c.Rules = append(c.Rules, config.Rule{ID: "pin", Domains: []string{"public.example"}, Outbound: "direct"})
		}, "direct", nil},
		{"candidate removed", "warp", func(c *config.Config, _ map[string]outbound.Provider) {
			c.Adaptive.Candidates = []string{"direct"}
		}, "direct", nil},
		{"candidate provider rebuilt", "warp", func(_ *config.Config, p map[string]outbound.Provider) {
			p["warp"] = provider("warp")
		}, "direct", nil},
		{"default initial outbound changed", "direct", func(c *config.Config, _ map[string]outbound.Provider) {
			c.DefaultOutbound = "warp"
		}, "warp", nil},
		{"adaptive rule initial outbound changed", "direct", func(c *config.Config, _ map[string]outbound.Provider) {
			c.Rules[0].Outbound = "warp"
		}, "warp", adaptiveRule},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e, _ := testEngine()
			providers := map[string]outbound.Provider{"direct": provider("direct"), "warp": provider("warp"), "private": provider("private")}
			base := cloneConfig(e.cfg)
			if tt.base != nil {
				tt.base(&base)
			}
			e.Update(base, providers)
			learn(e, tt.learned)
			next := cloneConfig(e.cfg)
			tt.change(&next, providers)
			e.Update(next, providers)
			if got := route(e); got != tt.want {
				t.Fatalf("route after update = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDisabledAdaptiveKeepsRuleAndAddressFamily(t *testing.T) {
	e, _ := testEngine()
	e.cfg.Adaptive.Enabled = false
	e.cfg.Rules = []config.Rule{{ID: "manual-auto", Domains: []string{"example.com"}, Outbound: "warp", Family: "ipv4", Adaptive: true, Candidates: []string{"direct", "warp"}}}
	e.Update(e.cfg, e.providers)
	r, err := e.selectRoute(target{host: "example.com"})
	if err != nil || r.outbound != "warp" || r.family != "ipv4" || len(r.candidates) != 0 {
		t.Fatalf("disabled adaptive ignored initial rule: %+v %v", r, err)
	}
}

func TestUnknownRemoteIPIsNeverInvented(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = a.Close(); _ = b.Close() }()
	if ip := destinationIP(a, "example.com"); ip != "" {
		t.Fatalf("invented destination: %s", ip)
	}
	if ip := destinationIP(a, "203.0.113.2"); ip != "203.0.113.2" {
		t.Fatalf("lost literal IP: %s", ip)
	}
}

func TestMeasurementsSeparateConfirmedFamilies(t *testing.T) {
	e, _ := testEngine()
	d := e.destinationLocked("x", "example.com:443", "tcp", route{outbound: "direct", candidates: []string{"direct", "warp"}})
	e.recordFamilyLocked(d, "direct", 100*time.Millisecond, nil, "ipv4")
	e.recordFamilyLocked(d, "direct", 200*time.Millisecond, nil, "ipv6")
	e.recordFamilyLocked(d, "warp", 80*time.Millisecond, nil, "unknown")
	e.recordFamilyLocked(d, "warp", 0, context.Canceled, "unknown")
	m := e.Snapshot().Destinations[0].Measurements
	if len(m) != 3 || m[0].Family != "ipv4" || m[1].Family != "ipv6" || m[2].Family != "unknown" || m[2].Samples != 1 {
		t.Fatalf("families were mixed or cancellation counted: %+v", m)
	}
}

func TestEngineCloseCancelsOutstandingProbe(t *testing.T) {
	started := make(chan struct{}, 2)
	blocking := func(ctx context.Context, _, _ string) (net.Conn, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	cfg := config.Config{DefaultOutbound: "direct", Outbounds: []config.Outbound{{ID: "direct", Type: "direct", Enabled: true}, {ID: "warp", Type: "warp", Enabled: true}}, Adaptive: config.Adaptive{Enabled: true, Candidates: []string{"direct", "warp"}}}
	e := New(cfg, map[string]outbound.Provider{"direct": testProvider{id: "direct", dial: blocking}, "warp": testProvider{id: "warp", dial: blocking}})
	defer func() { _ = e.Close() }()
	e.mu.Lock()
	e.destinationLocked("x", "example.com:443", "tcp", route{rule: "default", family: "auto", outbound: "direct", candidates: []string{"direct", "warp"}})
	e.mu.Unlock()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not start")
	}
	done := make(chan struct{})
	go func() { _ = e.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("probe ignored shutdown")
	}
	if _, err := e.DialContext(context.Background(), "tcp", "example.com:443"); err == nil {
		t.Fatal("closed engine accepted a stream")
	}
}

func FuzzRuleHostname(f *testing.F) {
	for _, s := range []string{"github.com", "evilgithub.com", "[::1]", "x\x00.example", "公司.example"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, host string) {
		r := config.Rule{Suffixes: []string{"github.com"}, CIDRs: []string{"10.0.0.0/8"}}
		_ = compileTarget(r).match(target{host: normalize(host)})
		_ = privateHost(host, config.PAC{})
	})
}

func TestAdaptiveHonorsZeroAbsoluteImprovement(t *testing.T) {
	e, now := testEngine()
	e.cfg.Adaptive = config.Default(t.TempDir()).Adaptive
	e.cfg.Adaptive.ImprovementMillis = 0
	d := e.destinationLocked("zero", "public.example:443", "tcp", route{outbound: "direct", candidates: []string{"direct", "warp"}})
	d.switched = now.Add(-11 * time.Minute)
	for range 3 {
		e.recordLocked(d, "direct", 40*time.Millisecond, nil)
	}
	for range 3 {
		e.recordLocked(d, "warp", 20*time.Millisecond, nil)
	}
	if d.current != "direct" {
		t.Fatal("switched without two rounds")
	}
	e.recordLocked(d, "warp", 20*time.Millisecond, nil)
	if d.current != "warp" {
		t.Fatal("configured zero millisecond threshold was replaced by the default")
	}
}

func TestAdaptiveETLDPlusOneAffinityAndSubdomainSharing(t *testing.T) {
	e, now := testEngine()
	var dialed []string
	var mu sync.Mutex
	provider := func(id string, d time.Duration) outbound.Provider {
		return testProvider{id: id, dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, id+":"+addr)
			mu.Unlock()
			*now = now.Add(d)
			a, b := net.Pipe()
			_ = b.Close()
			return a, nil
		}}
	}
	e.Update(e.cfg, map[string]outbound.Provider{
		"direct":  provider("direct", 200*time.Millisecond),
		"warp":    provider("warp", 20*time.Millisecond),
		"private": provider("private", 10*time.Millisecond),
	})

	// Dialing chatgpt.com, ws.chatgpt.com, and ab.chatgpt.com must share a single eTLD+1 adaptive destination.
	for _, host := range []string{"chatgpt.com:443", "ws.chatgpt.com:443", "ab.chatgpt.com:443"} {
		c, err := e.DialContext(t.Context(), "tcp", host)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
	}
	snap := e.Snapshot()
	if len(snap.Destinations) != 1 || snap.Destinations[0].Address != "chatgpt.com:443" {
		t.Fatalf("expected single eTLD+1 destination chatgpt.com:443, got %+v", snap.Destinations)
	}

	// Once chatgpt.com switches to warp, both chatgpt.com and ws.chatgpt.com must use warp.
	e.mu.Lock()
	d := e.destinations["chatgpt.com:443|tcp"]
	if d == nil || d.probeAddress != "ab.chatgpt.com:443" {
		e.mu.Unlock()
		t.Fatalf("unexpected adaptive state: %+v", d)
	}
	d.switched = now.Add(-11 * time.Minute)
	for range 3 {
		e.recordLocked(d, "warp", 20*time.Millisecond, nil)
	}
	e.recordLocked(d, "warp", 20*time.Millisecond, nil)
	e.mu.Unlock()

	mu.Lock()
	dialed = nil
	mu.Unlock()
	for _, host := range []string{"chatgpt.com:443", "ws.chatgpt.com:443"} {
		c, err := e.DialContext(t.Context(), "tcp", host)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
	}
	mu.Lock()
	got := append([]string(nil), dialed...)
	mu.Unlock()
	want := []string{"warp:chatgpt.com:443", "warp:ws.chatgpt.com:443"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("subdomains split across outbounds: got %v, want %v", got, want)
	}
}

func TestAdaptivePerDialImmediateFallbackAndFixedStrictIsolation(t *testing.T) {
	e, _ := testEngine()
	e.cfg.DefaultOutbound = "warp"
	e.cfg.Adaptive.Candidates = []string{"warp", "direct"}
	e.cfg.Rules = []config.Rule{
		{ID: "fixed-warp", Domains: []string{"strict.example"}, Outbound: "warp"},
	}
	var warpFastFlags []bool
	var mu sync.Mutex
	e.Update(e.cfg, map[string]outbound.Provider{
		"warp": testProvider{id: "warp", dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			mu.Lock()
			warpFastFlags = append(warpFastFlags, routeinfo.From(ctx).FastFailover)
			mu.Unlock()
			return nil, outbound.ErrWARPReset
		}},
		"direct": testProvider{id: "direct", dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			if routeinfo.From(ctx).FastFailover {
				t.Error("last fallback candidate should not have FastFailover set")
			}
			a, b := net.Pipe()
			_ = b.Close()
			return a, nil
		}},
		"private": testProvider{id: "private"},
	})

	// 1. Adaptive route immediately falls back to direct on the same dial when warp fails with ErrWARPReset.
	c, err := e.DialContext(t.Context(), "tcp", "ws.chatgpt.com:443")
	if err != nil {
		t.Fatalf("adaptive dial did not fall back to direct: %v", err)
	}
	_ = c.Close()
	snap := e.Snapshot()
	if len(snap.Flows) != 1 || snap.Flows[0].Outbound != "direct" {
		t.Fatalf("expected flow on fallback outbound direct, got %+v", snap.Flows)
	}

	// 2. Fixed route NEVER falls back to direct and passes FastFailover == false.
	if _, err := e.DialContext(t.Context(), "tcp", "strict.example:443"); !errors.Is(err, outbound.ErrWARPReset) {
		t.Fatalf("fixed route should fail with ErrWARPReset without falling back, got %v", err)
	}
	mu.Lock()
	flags := append([]bool(nil), warpFastFlags...)
	mu.Unlock()
	if len(flags) != 2 || !flags[0] || flags[1] {
		t.Fatalf("unexpected FastFailover flags [adaptive, fixed]: %v", flags)
	}
}

func TestScheduleProbesSuppressesFreshPassiveCandidates(t *testing.T) {
	e, now := testEngine()
	e.cfg.DefaultOutbound = "warp"
	e.cfg.Adaptive.Candidates = []string{"warp", "direct"}
	d := e.destinationLocked("chatgpt.com:443|tcp", "chatgpt.com:443", "tcp", route{rule: "default", family: "auto", outbound: "warp", candidates: []string{"warp", "direct"}})
	d.probeAddress = "ws.chatgpt.com:443"
	// Record a fresh passive sample on "warp" (< 1 minute old); "direct" has no sample yet.
	e.recordLocked(d, "warp", 30*time.Millisecond, nil)

	jobs := e.scheduleProbes()
	if len(jobs) != 1 || jobs[0].id != "direct" || jobs[0].address != "ws.chatgpt.com:443" {
		t.Fatalf("expected probe to skip fresh warp and probe standby direct at ws.chatgpt.com:443, got %+v", jobs)
	}

	// Once "direct" also has a fresh sample (< 1 minute old), no probe should be scheduled even after lastProbe resets.
	e.probes = 0
	e.recordLocked(d, "direct", 25*time.Millisecond, nil)
	d.lastProbe = now.Add(-2 * time.Minute)
	if got := e.scheduleProbes(); len(got) != 0 {
		t.Fatalf("expected 0 probes when all candidates have fresh passive samples, got %+v", got)
	}
}

func TestEngineUDPRoutingAndFlowAccounting(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	go func() {
		buf := make([]byte, 1024)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], addr)
		}
	}()

	e, _ := testEngine()
	e.providers["direct"] = testProvider{id: "direct", dial: (&net.Dialer{}).DialContext}
	c, err := e.DialContext(t.Context(), "udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	res := make([]byte, 16)
	n, err := c.Read(res)
	if err != nil || string(res[:n]) != "ping" {
		t.Fatalf("unexpected UDP echo: %q, %v", res[:n], err)
	}

	s := e.Snapshot()
	if len(s.Flows) != 1 || s.Flows[0].Network != "udp" || s.Flows[0].Outbound != "direct" || s.Flows[0].UploadBytes != 4 || s.Flows[0].DownloadBytes != 4 {
		t.Fatalf("flow accounting mismatch: %+v", s.Flows)
	}

	// Fixed routing to an unavailable provider must fail, not silently fallback to direct.
	cfg := cloneConfig(e.cfg)
	cfg.Rules = []config.Rule{{ID: "vpn-only", Ports: []string{"53"}, Outbound: "vpn"}}
	e.Update(cfg, map[string]outbound.Provider{"direct": testProvider{id: "direct"}})
	_, err = e.DialContext(t.Context(), "udp", "192.0.2.1:53")
	if err == nil || !strings.Contains(err.Error(), "no fallback") {
		t.Fatalf("expected no fallback error for unavailable provider, got: %v", err)
	}

	// Unsupported network
	if _, err := e.DialContext(t.Context(), "unix", pc.LocalAddr().String()); err == nil {
		t.Fatal("expected error for unsupported network")
	}
}

func TestEngineUDPFailuresCountedWithoutChangingHealth(t *testing.T) {
	e, _ := testEngine()
	refuseUDP := func(_ context.Context, network, _ string) (net.Conn, error) {
		if strings.HasPrefix(network, "udp") {
			return nil, fmt.Errorf("WARP Local Proxy: %w", outbound.ErrUDPUnsupported)
		}
		return nil, syscall.ECONNREFUSED
	}
	providers := map[string]outbound.Provider{
		"direct": testProvider{id: "direct"},
		"warp":   testProvider{id: "warp", dial: refuseUDP},
		"off": testProvider{id: "off", dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, fmt.Errorf("outbound off %w: disabled", outbound.ErrUnavailable)
		}},
		"slow":     testProvider{id: "slow", dial: func(context.Context, string, string) (net.Conn, error) { return nil, context.DeadlineExceeded }},
		"canceled": testProvider{id: "canceled", dial: func(context.Context, string, string) (net.Conn, error) { return nil, context.Canceled }},
		"loop": testProvider{id: "loop", dial: func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			t.Cleanup(func() { _ = server.Close() })
			return client, nil
		}},
	}
	cfg := cloneConfig(e.cfg)
	cfg.Adaptive.Enabled = false
	cfg.OutboundGroups = []config.OutboundGroup{{ID: "paused", Type: "fallback", Members: []string{"direct"}, Enabled: false}}
	cfg.Rules = []config.Rule{
		{ID: "quic", Ports: []string{"443"}, Outbound: "warp"},
		{ID: "missing", Ports: []string{"53"}, Outbound: "vpn"},
		{ID: "off", Ports: []string{"54"}, Outbound: "off"},
		{ID: "slow", Ports: []string{"55"}, Outbound: "slow"},
		{ID: "canceled", Ports: []string{"56"}, Outbound: "canceled"},
		{ID: "paused", Ports: []string{"57"}, Outbound: "paused"},
		{ID: "loop", Ports: []string{"58"}, Outbound: "loop"},
		{ID: "company", Domains: []string{"company.example"}, Outbound: "warp"},
	}
	e.Update(cfg, providers)
	// net.Pipe reports "pipe" as its peer: here that is this daemon's listener.
	e.SetDestinationPolicy(func(address string) (bool, bool) { return address == "pipe", false })
	for range 3 {
		if _, err := e.DialContext(t.Context(), "udp", "192.0.2.1:443"); !errors.Is(err, outbound.ErrUDPUnsupported) {
			t.Fatalf("UDP via WARP: %v", err)
		}
	}
	for _, address := range []string{"192.0.2.1:53", "192.0.2.1:54", "192.0.2.1:55", "192.0.2.1:56", "192.0.2.1:57", "192.0.2.1:58", "company.example:443"} {
		if _, err := e.DialContext(t.Context(), "udp", address); err == nil {
			t.Fatalf("UDP to %s unexpectedly connected", address)
		}
	}
	if health := e.health["warp"]; health != nil {
		t.Fatalf("UDP refusals changed WARP health: %+v", health)
	}
	if _, err := e.DialContext(t.Context(), "tcp", "192.0.2.1:443"); err == nil {
		t.Fatal("TCP refusal unexpectedly connected")
	}
	if health := e.health["warp"]; health == nil || health.consecutiveFailures != 1 {
		t.Fatalf("TCP health must count only TCP attempts: %+v", health)
	}
	want := []UDPDialFailure{
		{Outbound: "loop", Reason: UDPFailureBlocked, Count: 1},
		{Outbound: "off", Reason: UDPFailureUnavailable, Count: 1},
		{Outbound: "paused", Reason: UDPFailureUnavailable, Count: 1},
		{Outbound: "slow", Reason: UDPFailureTimeout, Count: 1},
		{Outbound: "vpn", Reason: UDPFailureUnavailable, Count: 1},
		{Outbound: "warp", Reason: UDPFailureBlocked, Count: 1}, // private-network protection
		{Outbound: "warp", Reason: UDPFailureUnsupported, Count: 3},
	}
	if got := e.Snapshot().UDPDialFailures; !slices.Equal(got, want) {
		t.Fatalf("UDP failures = %+v, want %+v (TCP and cancellation excluded)", got, want)
	}
	delete(providers, "warp")
	delete(providers, "loop")
	cfg.OutboundGroups = nil
	cfg.Rules = nil
	e.Update(cfg, providers)
	want = []UDPDialFailure{{Outbound: "off", Reason: UDPFailureUnavailable, Count: 1}, {Outbound: "slow", Reason: UDPFailureTimeout, Count: 1}}
	if got := e.Snapshot().UDPDialFailures; !slices.Equal(got, want) {
		t.Fatalf("removed outbounds kept UDP failure counters: %+v", got)
	}
	providers["direct"] = testProvider{id: "direct", dial: func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		t.Cleanup(func() { _ = server.Close() })
		return client, nil
	}}
	e.Update(cfg, providers)
	e.SetDestinationPolicy(nil)
	conn, err := e.DialContext(t.Context(), "tcp", "192.0.2.1:80")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if flows := e.Snapshot().Flows; len(flows) != 1 || flows[0].Network != "tcp" {
		t.Fatalf("TCP flow network = %+v", flows)
	}
}
