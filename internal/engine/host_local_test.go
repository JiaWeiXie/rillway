package engine

import (
	"errors"
	"net"
	"net/netip"
	"rillway/internal/config"
	"rillway/internal/hostlocal"
	"rillway/internal/outbound"
	"rillway/internal/routeinfo"
	"testing"
	"time"
)

func TestHostLocalDirectRuntimeDecisionsAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		mode, source         string
		denied, hypothetical bool
	}{
		{hostlocal.LocalClientsOnly, "192.0.2.2", true, true},
		{hostlocal.Strict, "127.0.0.1", true, false},
		{hostlocal.Open, "192.0.2.2", false, true},
		{hostlocal.LocalClientsOnly, "::1", false, false},
	} {
		t.Run(tc.mode+tc.source, func(t *testing.T) {
			l, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = l.Close() }()
			_, port, _ := net.SplitHostPort(l.Addr().String())
			c := config.Default(t.TempDir())
			c.Outbounds, c.Rules, c.Adaptive.Candidates = []config.Outbound{{ID: "direct", Type: "direct", Enabled: true, PublicInternet: true}}, nil, []string{"direct"}
			p, err := outbound.New(t.Context(), c.Outbounds[0])
			if err != nil {
				t.Fatal(err)
			}
			e := New(c, map[string]outbound.Provider{"direct": p})
			defer func() { _ = e.Close() }()
			policy, err := hostlocal.Compile(tc.mode, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			state := hostlocal.NewState(policy)
			e.SetHostLocalPolicy(state)
			source := netip.MustParseAddr(tc.source)
			event := state.Begin(source, netip.MustParseAddrPort(l.Addr().String()).Port())
			ctx := hostlocal.WithEvent(routeinfo.With(t.Context(), routeinfo.Info{SourceIP: source, Inbound: "http"}), event)
			conn, err := e.DialContext(ctx, "tcp4", "localhost:"+port)
			event.Complete(err)
			if errors.Is(err, hostlocal.ErrDenied) != tc.denied {
				t.Fatalf("denied=%v err=%v", tc.denied, err)
			}
			if !tc.denied {
				if err != nil {
					t.Fatal(err)
				}
				verified, ok := conn.(interface{ HostDestination() (netip.AddrPort, bool) }).HostDestination()
				if !ok || verified.String() != l.Addr().String() {
					t.Fatal("lost verified direct identity", verified)
				}
				_ = conn.Close()
				peer, err := l.Accept()
				if err != nil {
					t.Fatal(err)
				}
				_ = peer.Close()
			} else if len(e.Snapshot().Flows) != 0 || len(e.Snapshot().Destinations) != 0 {
				t.Fatal("refused traffic was observed")
			}
			snapshot := state.Snapshot()
			if (snapshot.ActualDenials == 1) != tc.denied || (snapshot.HypotheticalLocalClientsOnlyDenials == 1) != tc.hypothetical {
				t.Fatal(snapshot)
			}
			preview, err := e.Explain(config.ExplainRequest{Host: "localhost", Port: port, Source: tc.source})
			if err != nil || preview.Blocked || preview.HostLocalResolution != "pending_direct_resolution" {
				t.Fatal("preview implied resolution or refused conditional direct check", preview, err)
			}
			if after := state.Snapshot(); after.ActualDenials != snapshot.ActualDenials || after.HypotheticalLocalClientsOnlyDenials != snapshot.HypotheticalLocalClientsOnlyDenials {
				t.Fatal("Explain counted traffic")
			}
		})
	}
}

func TestHostLocalProbeRetainsSourceAndDoesNotPenalizeProvider(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	e, _ := testEngine()
	p, err := outbound.New(t.Context(), config.Outbound{ID: "direct", Type: "direct", Enabled: true, PublicInternet: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	e.providers["direct"] = p
	policy, err := hostlocal.Compile(hostlocal.LocalClientsOnly, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := hostlocal.NewState(policy)
	e.SetHostLocalPolicy(state)
	d := e.destinationLocked("probe", "localhost:"+port, "tcp4", route{outbound: "direct", candidates: []string{"direct", "warp"}})
	e.probes = 1
	job := probeJob{key: "probe", id: "direct", address: "localhost:" + port, network: "tcp4", state: d, provider: p, source: netip.MustParseAddr("192.0.2.2"), inbound: "http", providerType: "direct", generation: e.generation}
	conn, err := e.dialProbe(t.Context(), job)
	if conn != nil {
		_ = conn.Close()
		t.Fatal("probe bypassed source-aware guard")
	}
	if !errors.Is(err, hostlocal.ErrDenied) {
		t.Fatal("probe lost policy identity", err)
	}
	e.completeProbe(job, e.now(), conn, err)
	if e.probes != 0 || e.health["direct"] != nil || len(d.samples) != 0 || len(e.flows) != 0 {
		t.Fatal("policy refusal polluted health/samples or leaked probe budget")
	}
	if got := state.Snapshot(); got.ActualDenials != 0 || got.HypotheticalLocalClientsOnlyDenials != 0 {
		t.Fatal("probe counted as ingress", got)
	}
	if err := l.(*net.TCPListener).SetDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if conn, err := l.Accept(); err == nil {
		_ = conn.Close()
		t.Fatal("probe refusal came after connect")
	}
	// Actual transport failures must still influence health and adaptive samples.
	e.recordHealthLocked("direct", errors.New("synthetic network failure"))
	e.recordFamilyLocked(d, "direct", time.Millisecond, errors.New("synthetic network failure"), "ipv4")
	if e.health["direct"].consecutiveFailures != 1 || len(d.samples["direct"]) != 1 {
		t.Fatal("ordinary transport failure was suppressed")
	}
}
