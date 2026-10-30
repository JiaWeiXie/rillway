package engine

import (
	"context"
	"io"
	"net"
	"rillway/internal/config"
	"strconv"
	"testing"
	"time"
)

func TestManagementEndpointIsRefusedNotRouted(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	e, _ := testEngine()
	e.SetDestinationPolicy(func(address string) string {
		if address == l.Addr().String() {
			return "management_endpoint"
		}
		return ""
	})
	if c, err := e.DialContext(t.Context(), "tcp", l.Addr().String()); err == nil {
		_ = c.Close()
		t.Fatal("management endpoint was dialed through the proxy")
	}
	_ = l.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond))
	if peer, err := l.Accept(); err == nil {
		_ = peer.Close()
		t.Fatal("the refused endpoint received a connection")
	}
	if snap := e.Snapshot(); len(snap.Flows) != 0 || len(snap.Destinations) != 0 || snap.Totals.DownloadBytes != 0 {
		t.Fatal("refused traffic was observed", snap)
	}
	explain, err := e.Explain(config.ExplainRequest{Host: "127.0.0.1", Port: strconv.Itoa(l.Addr().(*net.TCPAddr).Port)})
	if err != nil || !explain.Blocked || explain.Reason != "management_endpoint" {
		t.Fatalf("route preview: %+v %v", explain, err)
	}
	if _, err = e.DialContext(t.Context(), "tcp", "127.0.0.1:1"); err == nil {
		t.Fatal("unrelated private destination bypassed fixed routing")
	}
}

func TestResolvedSelfEndpointDoesNotCreateFlowsOrAdaptiveState(t *testing.T) {
	for _, reason := range []string{"proxy_listener", "management_endpoint"} {
		t.Run(reason, func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = l.Close() }()
			e, _ := testEngine()
			e.providers["direct"] = testProvider{id: "direct", dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != "alias.invalid:443" {
					t.Fatal("unexpected provider address", address)
				}
				if len(e.scheduleProbes()) != 0 || len(e.Snapshot().Destinations) != 0 {
					t.Fatal("adaptive destination/probe published before self resolution")
				}
				return (&net.Dialer{}).DialContext(ctx, network, l.Addr().String())
			}}
			e.SetDestinationPolicy(func(address string) string {
				if address == l.Addr().String() {
					return reason
				}
				return ""
			})
			c, err := e.DialContext(t.Context(), "tcp", "alias.invalid:443")
			if err == nil {
				_ = c.Close()
				t.Fatal("alias resolving to an own listener was accepted")
			}
			peer, err := l.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = peer.Close() }()
			_ = peer.SetReadDeadline(time.Now().Add(time.Second))
			if _, err = peer.Read(make([]byte, 1)); err != io.EOF {
				t.Fatal("self endpoint retained an open connection", err)
			}
			if snap := e.Snapshot(); len(snap.Flows) != 0 || len(snap.Destinations) != 0 || snap.Totals.ActiveConnections != 0 {
				t.Fatal("resolved self traffic polluted observations", snap)
			}
			if want := map[string]uint64{"proxy_listener": 0, "management_endpoint": 1}[reason]; e.ManagementRefusals() != want {
				t.Fatalf("ManagementRefusals = %d for %s, want %d", e.ManagementRefusals(), reason, want)
			}
		})
	}
}

func TestUnrelatedResolvedDestinationRemainsObservable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	e, _ := testEngine()
	e.providers["direct"] = testProvider{id: "direct", dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, l.Addr().String())
	}}
	e.SetDestinationPolicy(func(string) string { return "" })
	c, err := e.DialContext(t.Context(), "tcp", "ordinary.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	peer, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	_ = peer.Close()
	if snap := e.Snapshot(); len(snap.Flows) != 1 || len(snap.Destinations) != 1 {
		t.Fatal("ordinary destination lost observation or adaptive state", snap)
	}
}

func TestOldConfigurationDialCannotPublishAdaptiveDestination(t *testing.T) {
	e, _ := testEngine()
	e.providers["direct"] = testProvider{id: "direct", dial: func(context.Context, string, string) (net.Conn, error) {
		e.Update(e.cfg, e.providers)
		return nil, context.DeadlineExceeded
	}}
	_, _ = e.DialContext(t.Context(), "tcp", "ordinary.invalid:443")
	if snap := e.Snapshot(); len(snap.Destinations) != 0 {
		t.Fatal("old dial published observations into new policy", snap)
	}
}

func TestProbeResolvingToSelfIsDiscardedAndClosed(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	e, _ := testEngine()
	key := "alias.invalid:443|tcp"
	r, err := e.selectRoute(target{host: "alias.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	state := e.destinationLocked(key, "alias.invalid:443", "tcp", r)
	e.probes = 1
	e.SetDestinationPolicy(func(address string) string {
		if address == l.Addr().String() {
			return "management_endpoint"
		}
		return ""
	})
	c, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	e.completeProbe(probeJob{key: key, address: state.address, id: "direct", state: state}, e.now(), c, nil)
	peer, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = peer.Read(make([]byte, 1)); err != io.EOF {
		t.Fatal("probe connection left open", err)
	}
	if e.probes != 0 || len(e.Snapshot().Destinations) != 0 || len(state.samples) != 0 {
		t.Fatal("internal probe polluted adaptive data or leaked budget")
	}
}
