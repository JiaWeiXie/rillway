package engine

import (
	"context"
	"errors"
	"io"
	"net"
	"rillway/internal/config"
	"rillway/internal/outbound"
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
	for _, tt := range []struct{ host, rule string }{{"api.example.com", "fixed"}, {"cdn.githubassets.com", "suffix"}, {"githubassets.com", "suffix"}, {"evilgithubassets.com", "default"}, {"203.0.113.10", "cidr"}, {"other.example.com", "auto"}} {
		t.Run(tt.host, func(t *testing.T) {
			r, err := e.selectRoute(tt.host)
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
		if _, err := e.selectRoute(host); err == nil {
			t.Errorf("private host allowed: %s", host)
		}
	}
	e.cfg.Rules = []config.Rule{{ID: "company", Suffixes: []string{"company.example"}, Outbound: "private"}}
	r, err := e.selectRoute("service.company.example")
	if err != nil || r.outbound != "private" || len(r.candidates) != 0 {
		t.Fatalf("private route: %+v %v", r, err)
	}
}

func TestFixedWireGuardCanServePrivateNetwork(t *testing.T) {
	e, _ := testEngine()
	e.cfg.Outbounds = append(e.cfg.Outbounds, config.Outbound{ID: "company-vpn", Type: "wireguard", Enabled: true, PublicInternet: true})
	e.providers["company-vpn"] = testProvider{id: "company-vpn"}
	e.cfg.Rules = []config.Rule{{ID: "company", Suffixes: []string{"company.example"}, Outbound: "company-vpn"}}
	r, err := e.selectRoute("service.company.example")
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
		if _, err := e.selectRoute(host); err == nil {
			t.Errorf("private destination sent to default public WireGuard: %s", host)
		}
	}
	e.cfg.Adaptive.Enabled = false
	e.cfg.Rules = []config.Rule{{ID: "adaptive-private", Suffixes: []string{"company.example"}, Outbound: "public-vpn", Adaptive: true, Candidates: []string{"direct", "public-vpn"}}}
	if _, err := e.selectRoute("service.company.example"); err == nil {
		t.Fatal("disabled adaptive rule treated as explicit fixed private route")
	}
}

func TestUnavailableFixedOutboundDoesNotFallback(t *testing.T) {
	e, _ := testEngine()
	e.cfg.Rules = []config.Rule{{ID: "missing", Domains: []string{"github.com"}, Outbound: "missing"}}
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
		e.destinationLocked(key, host, "tcp", route{outbound: "direct", candidates: []string{"direct", "warp"}})
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

func TestDisabledAdaptiveKeepsRuleAndAddressFamily(t *testing.T) {
	e, _ := testEngine()
	e.cfg.Adaptive.Enabled = false
	e.cfg.Rules = []config.Rule{{ID: "manual-auto", Domains: []string{"example.com"}, Outbound: "warp", Family: "ipv4", Adaptive: true, Candidates: []string{"direct", "warp"}}}
	r, err := e.selectRoute("example.com")
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
	e.destinationLocked("x", "example.com:443", "tcp", route{outbound: "direct", candidates: []string{"direct", "warp"}})
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
		_ = matches(r, normalize(host))
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
