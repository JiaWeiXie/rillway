package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"rillway/internal/config"
	"rillway/internal/outbound"
	"rillway/internal/routeinfo"
	"slices"
	"syscall"
	"testing"
	"time"
)

type groupHarness struct {
	e         *Engine
	now       *time.Time
	cfg       config.Config
	providers map[string]outbound.Provider
	attempts  []string
	dials     map[string]func(context.Context, string, string) (net.Conn, error)
	lastConn  net.Conn
}

func newGroupHarness(g config.OutboundGroup) *groupHarness {
	e, now := testEngine()
	g.ID = "group"
	g.Enabled = true
	g.Members = []string{"A", "B"}
	h := &groupHarness{e: e, now: now, cfg: cloneConfig(e.cfg), providers: make(map[string]outbound.Provider), dials: make(map[string]func(context.Context, string, string) (net.Conn, error))}
	h.cfg.Adaptive.Enabled = false
	h.cfg.Rules = nil
	h.cfg.Outbounds = []config.Outbound{{ID: "direct", Type: "direct", Enabled: true}, {ID: "A", Type: "warp", Enabled: true}, {ID: "B", Type: "warp", Enabled: true}}
	h.cfg.DefaultOutbound = g.ID
	h.cfg.OutboundGroups = []config.OutboundGroup{g}
	for _, id := range []string{"direct", "A", "B"} {
		h.providers[id] = &testProvider{id: id, dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			h.attempts = append(h.attempts, id)
			if dial := h.dials[id]; dial != nil {
				return dial(ctx, network, address)
			}
			conn, peer := net.Pipe()
			_ = peer.Close()
			h.lastConn = conn
			return conn, nil
		}}
	}
	e.Update(h.cfg, h.providers)
	return h
}

func (h *groupHarness) dial(t *testing.T, ctx context.Context, address string) string {
	t.Helper()
	conn, err := h.e.DialContext(ctx, "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	flows := h.e.Snapshot().Flows
	if len(flows) == 0 {
		t.Fatal("successful group dial did not publish a flow")
	}
	return flows[0].Outbound
}

func (h *groupHarness) order(t *testing.T, dst target, fixed bool) []string {
	t.Helper()
	h.e.mu.Lock()
	defer h.e.mu.Unlock()
	order, err := h.e.groupAttemptOrderLocked(h.e.groups["group"], dst, fixed, h.e.now())
	if err != nil {
		t.Fatal(err)
	}
	return order
}

func refuseGroupMember(context.Context, string, string) (net.Conn, error) {
	return nil, syscall.ECONNREFUSED
}

func TestGroupFallbackRetriesAndRecordsActualMember(t *testing.T) {
	h := newGroupHarness(config.OutboundGroup{Type: "fallback"})
	h.dials["A"] = refuseGroupMember
	h.cfg.Rules = []config.Rule{{ID: "group-rule", Domains: []string{"example.com"}, Outbound: "group"}}
	h.e.Update(h.cfg, h.providers)
	conn, err := h.e.DialContext(t.Context(), "tcp", "example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	tracked, ok := conn.(*trackedConn)
	if !ok || tracked.Conn != h.lastConn {
		t.Fatal("fallback did not return B's connection")
	}
	if !slices.Equal(h.attempts, []string{"A", "B"}) {
		t.Fatalf("attempts = %v, want [A B]", h.attempts)
	}
	flows := h.e.Snapshot().Flows
	if len(flows) != 1 || flows[0].Outbound != "B" || flows[0].Rule != "group-rule" {
		t.Fatalf("flow = %+v, want B and group-rule", flows)
	}
	if health := h.e.health["A"]; health == nil || health.consecutiveFailures != 1 || !health.lastFailure.Equal(*h.now) || !health.lastSuccess.IsZero() {
		t.Fatalf("A health = %+v, want one failure at the controlled time", health)
	}
	if health := h.e.health["B"]; health == nil || health.consecutiveFailures != 0 || !health.lastSuccess.Equal(*h.now) {
		t.Fatalf("B health = %+v, want success at the controlled time", health)
	}
}

func TestGroupFallbackPrefersHealthyButRetainsFailedMembers(t *testing.T) {
	h := newGroupHarness(config.OutboundGroup{Type: "fallback"})
	for range 3 {
		h.e.recordHealth("A", syscall.ECONNREFUSED)
	}
	if got := h.order(t, target{host: "example.com", port: "443"}, false); !slices.Equal(got, []string{"B", "A"}) {
		t.Fatalf("order = %v, want [B A]", got)
	}
	if got := h.dial(t, t.Context(), "example.com:443"); got != "B" || !slices.Equal(h.attempts, []string{"B"}) {
		t.Fatalf("member = %q, attempts = %v; want B only", got, h.attempts)
	}
	// Health is a preference: when B fails, previously unhealthy A is still tried.
	h.attempts = nil
	h.dials["B"] = refuseGroupMember
	*h.now = h.now.Add(time.Second)
	if got := h.dial(t, t.Context(), "example.com:443"); got != "A" || !slices.Equal(h.attempts, []string{"B", "A"}) {
		t.Fatalf("member = %q, attempts = %v; want [B A] then A success", got, h.attempts)
	}
	if h.e.health["A"].consecutiveFailures != 0 {
		t.Fatal("successful retry did not recover A's health")
	}
}

func TestGroupCancellationStopsBeforeNextMember(t *testing.T) {
	h := newGroupHarness(config.OutboundGroup{Type: "fallback"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	h.dials["A"] = func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		conn, err := h.e.DialContext(ctx, "tcp", "example.com:443")
		if conn != nil {
			_ = conn.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("dial returned before A started: %v", err)
	case <-time.After(time.Second):
		t.Fatal("A did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("dial did not stop after cancellation")
	}
	if !slices.Equal(h.attempts, []string{"A"}) || len(h.e.Snapshot().Flows) != 0 {
		t.Fatalf("cancelled request attempted %v or published a flow", h.attempts)
	}
	if health := h.e.health["A"]; health != nil && (!health.lastFailure.IsZero() || !health.lastSuccess.IsZero() || health.consecutiveFailures != 0) {
		t.Fatalf("cancellation changed health: %+v", health)
	}
}

func TestGroupSelectFailureDoesNotRetry(t *testing.T) {
	h := newGroupHarness(config.OutboundGroup{Type: "select", Selected: "A"})
	h.dials["A"] = refuseGroupMember
	conn, err := h.e.DialContext(t.Context(), "tcp", "example.com:443")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("failed select returned a connection")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) || !slices.Equal(h.attempts, []string{"A"}) {
		t.Fatalf("error = %v, attempts = %v; want ECONNREFUSED and A only", err, h.attempts)
	}
	if len(h.e.Snapshot().Flows) != 0 || h.e.health["A"] == nil || h.e.health["A"].consecutiveFailures != 1 {
		t.Fatal("failed select must record A's failure without a successful flow")
	}
}

func TestGroupConsistentHashingStableAcrossRequestsAndRebuiltEngine(t *testing.T) {
	g := config.OutboundGroup{Type: "load-balance", Strategy: "consistent-hashing", Key: "destination"}
	h := newGroupHarness(g)
	rebuilt := newGroupHarness(g)
	for _, host := range []string{"a.example.com", "b.example.com", "203.0.113.9"} {
		want := h.order(t, target{host: host, port: "443"}, false)
		for range 100 {
			if got := h.order(t, target{host: host, port: "443"}, false); !slices.Equal(got, want) {
				t.Fatalf("%s order changed: %v, want %v", host, got, want)
			}
		}
		if got := rebuilt.order(t, target{host: host, port: "443"}, false); !slices.Equal(got, want) {
			t.Fatalf("rebuilt engine changed %s order: %v, want %v", host, got, want)
		}
		for _, address := range []string{net.JoinHostPort(host, "443"), net.JoinHostPort(host, "8443")} {
			if got := h.dial(t, t.Context(), address); got != want[0] {
				t.Fatalf("%s selected %q, want %q", address, got, want[0])
			}
		}
	}
	if got := destinationKey(normalize("A.EXAMPLE.COM.")); got != "a.example.com" {
		t.Fatalf("destination key = %q", got)
	}
	if destinationKey("a.example.com") == destinationKey("b.example.com") {
		t.Fatal("destination keys collapsed distinct subdomains")
	}
	want := h.dial(t, t.Context(), "a.example.com:443")
	if got := h.dial(t, t.Context(), "A.EXAMPLE.COM.:443"); got != want {
		t.Fatalf("normalized host selected %q, want %q", got, want)
	}
}

func TestGroupConsistentHashingSourceAndUnknownSourceFallback(t *testing.T) {
	h := newGroupHarness(config.OutboundGroup{Type: "load-balance", Strategy: "consistent-hashing", Key: "source"})
	source := netip.MustParseAddr("203.0.113.7")
	want := h.order(t, target{host: "a.example.com", source: source}, false)
	for _, host := range []string{"a.example.com", "b.example.com"} {
		if got := h.order(t, target{host: host, source: source}, false); !slices.Equal(got, want) {
			t.Fatalf("same source changed order for %s: %v, want %v", host, got, want)
		}
		ctx := routeinfo.With(t.Context(), routeinfo.Info{SourceIP: source, Inbound: "socks5"})
		if got := h.dial(t, ctx, host+":443"); got != want[0] {
			t.Fatalf("source metadata selected %q, want %q", got, want[0])
		}
	}
	destination := newGroupHarness(config.OutboundGroup{Type: "load-balance", Strategy: "consistent-hashing", Key: "destination"})
	for _, host := range []string{"a.example.com", "b.example.com"} {
		got := h.order(t, target{host: host}, false)
		want := destination.order(t, target{host: host}, false)
		if !slices.Equal(got, want) {
			t.Fatalf("unknown-source order = %v, want destination order %v", got, want)
		}
	}
}

func TestGroupRoundRobinRotatesOncePerRequest(t *testing.T) {
	h := newGroupHarness(config.OutboundGroup{Type: "load-balance", Strategy: "round-robin"})
	for i, want := range []string{"A", "B", "A", "B", "A", "B"} {
		h.attempts = nil
		if got := h.dial(t, t.Context(), "example.com:443"); got != want || !slices.Equal(h.attempts, []string{want}) {
			t.Fatalf("request %d member = %q, attempts = %v; want %q only", i, got, h.attempts, want)
		}
		if got := h.e.groups["group"].counter.Load(); got != uint64(i+1) {
			t.Fatalf("request %d counter = %d, want %d", i, got, i+1)
		}
	}
}

func TestGroupRoundRobinRetryDoesNotAdvanceCursorAgain(t *testing.T) {
	h := newGroupHarness(config.OutboundGroup{Type: "load-balance", Strategy: "round-robin"})
	h.dials["A"] = refuseGroupMember
	if got := h.dial(t, t.Context(), "example.com:443"); got != "B" || !slices.Equal(h.attempts, []string{"A", "B"}) {
		t.Fatalf("first request member = %q, attempts = %v", got, h.attempts)
	}
	if got := h.e.groups["group"].counter.Load(); got != 1 {
		t.Fatalf("retry advanced counter to %d, want 1", got)
	}
	h.attempts = nil
	if got := h.dial(t, t.Context(), "example.com:443"); got != "B" || !slices.Equal(h.attempts, []string{"B"}) {
		t.Fatalf("second request member = %q, attempts = %v; want B only", got, h.attempts)
	}
	if got := h.e.groups["group"].counter.Load(); got != 2 {
		t.Fatalf("second request counter = %d, want 2", got)
	}
}

func TestGroupUpdatePreservesCursorAndResetsOnMemberOrderChange(t *testing.T) {
	h := newGroupHarness(config.OutboundGroup{Type: "load-balance", Strategy: "round-robin"})
	if got := h.dial(t, t.Context(), "example.com:443"); got != "A" {
		t.Fatalf("initial member = %q, want A", got)
	}
	h.e.Update(h.cfg, h.providers)
	if got := h.e.groups["group"].counter.Load(); got != 1 {
		t.Fatalf("unchanged update reset counter to %d", got)
	}
	if got := h.dial(t, t.Context(), "example.com:443"); got != "B" {
		t.Fatalf("unchanged update selected %q, want B", got)
	}
	h.cfg.Rules = []config.Rule{{ID: "unrelated", Domains: []string{"other.example"}, Outbound: "A"}}
	h.e.Update(h.cfg, h.providers)
	if got := h.e.groups["group"].counter.Load(); got != 2 {
		t.Fatalf("unrelated update reset counter to %d", got)
	}
	if got := h.dial(t, t.Context(), "example.com:443"); got != "A" {
		t.Fatalf("unrelated update selected %q, want A", got)
	}
	h.cfg.OutboundGroups[0].Members = []string{"B", "A"}
	h.e.Update(h.cfg, h.providers)
	if got := h.e.groups["group"].counter.Load(); got != 0 {
		t.Fatalf("member reorder counter = %d, want 0", got)
	}
	for _, want := range []string{"B", "A"} {
		if got := h.dial(t, t.Context(), "example.com:443"); got != want {
			t.Fatalf("reordered group member = %q, want %q", got, want)
		}
	}
}

func TestGroupPrivateDestinationsRejectWarpOnlyMembers(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		for _, host := range []string{"service.company.example", "nas", "box.local", "100.100.100.100", "127.0.0.1", "10.0.0.2", "::1", "fd00::1"} {
			t.Run(fmt.Sprintf("fixed=%t/%s", fixed, host), func(t *testing.T) {
				h := newGroupHarness(config.OutboundGroup{Type: "fallback"})
				if fixed {
					h.cfg.Rules = []config.Rule{{ID: "private-group", Ports: []string{"443"}, Outbound: "group"}}
					h.e.Update(h.cfg, h.providers)
				}
				conn, err := h.e.DialContext(t.Context(), "tcp", net.JoinHostPort(host, "443"))
				if conn != nil {
					_ = conn.Close()
					t.Fatal("private destination returned a connection")
				}
				if err == nil || len(h.attempts) != 0 || len(h.e.Snapshot().Flows) != 0 {
					t.Fatalf("private destination error = %v, attempts = %v", err, h.attempts)
				}
			})
		}
	}
}

func TestGroupHealthIgnoresCancellationAndRecoversOnSuccess(t *testing.T) {
	h := newGroupHarness(config.OutboundGroup{Type: "fallback"})
	h.e.recordHealth("A", context.Canceled)
	if got := h.e.health["A"]; got != nil {
		t.Fatalf("initial cancellation created health: %+v", got)
	}
	for i, err := range []error{syscall.ECONNREFUSED, errors.New("provider unavailable"), context.DeadlineExceeded} {
		*h.now = h.now.Add(time.Second)
		h.e.recordHealth("A", err)
		if got := h.e.health["A"]; got == nil || got.consecutiveFailures != i+1 || !got.lastFailure.Equal(*h.now) {
			t.Fatalf("health after failure %d = %+v", i+1, got)
		}
	}
	before := *h.e.health["A"]
	*h.now = h.now.Add(time.Second)
	h.e.recordHealth("A", fmt.Errorf("wrapped cancellation: %w", context.Canceled))
	if got := *h.e.health["A"]; got != before {
		t.Fatalf("wrapped cancellation changed health: %+v, want %+v", got, before)
	}
	if got := h.order(t, target{host: "example.com"}, false); !slices.Equal(got, []string{"B", "A"}) {
		t.Fatalf("failed member order = %v", got)
	}
	*h.now = h.now.Add(time.Second)
	h.e.recordHealth("A", nil)
	if got := h.e.health["A"]; got.consecutiveFailures != 0 || !got.lastSuccess.Equal(*h.now) || !got.lastFailure.Equal(before.lastFailure) {
		t.Fatalf("success did not recover health: %+v", got)
	}
	if got := h.order(t, target{host: "example.com"}, false); !slices.Equal(got, []string{"A", "B"}) {
		t.Fatalf("recovered member order = %v, want [A B]", got)
	}
}

func TestGroupProbeResultsRecordHealthAndIgnoreCancellation(t *testing.T) {
	h := newGroupHarness(config.OutboundGroup{Type: "fallback"})
	const key = "probe.example:443|tcp"
	h.e.mu.Lock()
	state := h.e.destinationLocked(key, "probe.example:443", "tcp", route{outbound: "A", candidates: []string{"A", "B"}})
	h.e.mu.Unlock()
	job := probeJob{key: key, id: "A", address: "probe.example:443", network: "tcp", state: state, provider: h.providers["A"]}
	complete := func(conn net.Conn, err error) {
		started := *h.now
		*h.now = h.now.Add(20 * time.Millisecond)
		h.e.mu.Lock()
		h.e.probes++
		h.e.mu.Unlock()
		h.e.completeProbe(job, started, conn, err)
		if h.e.probes != 0 {
			t.Fatalf("completed probe left %d outstanding probes", h.e.probes)
		}
	}
	for range 3 {
		complete(nil, syscall.ECONNREFUSED)
	}
	if got := h.e.health["A"]; got == nil || got.consecutiveFailures != 3 || !got.lastFailure.Equal(*h.now) {
		t.Fatalf("failed probes health = %+v", got)
	}
	if got := h.order(t, target{host: "example.com"}, false); !slices.Equal(got, []string{"B", "A"}) {
		t.Fatalf("probes did not deprioritize A: %v", got)
	}
	before := *h.e.health["A"]
	complete(nil, context.Canceled)
	if got := *h.e.health["A"]; got != before {
		t.Fatalf("cancelled probe changed health: %+v, want %+v", got, before)
	}
	conn, peer := net.Pipe()
	defer func() { _ = conn.Close(); _ = peer.Close() }()
	complete(conn, nil)
	_ = peer.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := peer.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("probe connection was not closed: %v", err)
	}
	if got := h.e.health["A"]; got.consecutiveFailures != 0 || !got.lastSuccess.Equal(*h.now) {
		t.Fatalf("successful probe did not recover health: %+v", got)
	}
	if got := h.order(t, target{host: "example.com"}, false); !slices.Equal(got, []string{"A", "B"}) {
		t.Fatalf("successful probe did not restore configured order: %v", got)
	}
}
