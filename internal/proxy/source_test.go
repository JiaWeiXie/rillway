package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"net/netip"
	"rillway/internal/access"
	"testing"
	"time"
)

func waitSource(t *testing.T, s *Server, predicate func(access.SourceClient) bool) access.SourceClient {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, row := range s.SourceClients().Clients {
			if predicate(row) {
				return row
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("source activity did not reach expected state")
	return access.SourceClient{}
}

func TestSourcePolicyPreParserSharedUpdateAndDisconnect(t *testing.T) {
	s := testServer(t, false)
	front := httptest.NewUnstartedServer(s.HTTPHandler())
	front.Listener = s.GuardListener(front.Listener)
	front.Start()
	defer front.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = s.ServeSOCKS(ctx, l) }()
	defer func() { _ = l.Close() }()
	dial := func(address string) net.Conn {
		t.Helper()
		c, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	held := dial(front.Listener.Addr().String())
	waitSource(t, s, func(r access.SourceClient) bool { return r.ActiveConnections == 1 })
	deny, err := access.Compile([]access.RuleSpec{{ID: "block", Action: "deny", CIDRs: []string{"127.0.0.1/32"}, Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	s.SetSourcePolicy(deny)
	// No protocol bytes sent: both listeners must reject at socket admission.
	for _, addr := range []string{front.Listener.Addr().String(), l.Addr().String()} {
		c := dial(addr)
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		var b [1]byte
		if _, err := c.Read(b[:]); err == nil {
			t.Fatal("denied socket stayed open")
		} else if e, ok := err.(net.Error); ok && e.Timeout() {
			t.Fatal("denied socket reached parser")
		}
	}
	row := waitSource(t, s, func(r access.SourceClient) bool { return r.DeniedConnections == 2 })
	if row.Decision != "deny" || row.RuleID != "block" || row.ActiveConnections != 1 || row.AcceptedConnections != 1 {
		t.Fatal(row)
	}
	// The ordinary update did not close the admitted socket.
	_ = held.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	var b [1]byte
	if _, err := held.Read(b[:]); err == nil {
		t.Fatal("unexpected data")
	} else if e, ok := err.(net.Error); !ok || !e.Timeout() {
		t.Fatal("ordinary update closed ingress", err)
	}
	if count := s.DisconnectSource(netip.MustParseAddr("::ffff:127.0.0.1")); count != 1 {
		t.Fatal(count)
	}
	if count := s.DisconnectSource(netip.MustParseAddr("127.0.0.1")); count != 0 {
		t.Fatal("repeat disconnect", count)
	}
	_ = held.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := held.Read(b[:]); err == nil {
		t.Fatal("disconnect left socket open")
	}
	row = waitSource(t, s, func(r access.SourceClient) bool { return r.ActiveConnections == 0 })
	if row.Address != "127.0.0.1" {
		t.Fatal(row)
	}
	s.SetSourcePolicy(testSourcePolicy(t))
	next := dial(l.Addr().String())
	_, _ = next.Write([]byte{5, 1, 0})
	_ = next.SetReadDeadline(time.Now().Add(time.Second))
	var reply [2]byte
	if _, err := io.ReadFull(next, reply[:]); err != nil || reply != [2]byte{5, 0} {
		t.Fatal(reply, err)
	}
}

func TestSourceRetentionExpiryActiveAndOrdering(t *testing.T) {
	g := NewConnectionGuard(256, 64, nil)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	activeIP := netip.MustParseAddr("192.0.2.1")
	active := g.record(activeIP, now.Add(-25*time.Hour))
	active.ActiveConnections = 1
	expiredIP := netip.MustParseAddr("192.0.2.2")
	g.record(expiredIP, now.Add(-25*time.Hour))
	snapshot := g.SourceClients()
	if len(snapshot.Clients) != 1 || snapshot.Clients[0].Address != activeIP.String() {
		t.Fatal(snapshot)
	}
	for i := range 1023 {
		g.record(netip.MustParseAddr(fmt.Sprintf("10.0.%d.%d", i/256, i%256)), now.Add(time.Duration(i)*time.Second))
	}
	oldest := netip.MustParseAddr("10.0.0.0")
	newest := netip.MustParseAddr("198.51.100.1")
	g.record(newest, now.Add(time.Hour))
	if len(g.sources) != 1024 || g.sources[activeIP] == nil || g.sources[oldest] != nil {
		t.Fatal("incorrect bounded eviction")
	}
	snapshot = g.SourceClients()
	if snapshot.Clients[0].Address != activeIP.String() || snapshot.Clients[1].Address != newest.String() {
		t.Fatal("incorrect snapshot order")
	}
	now = now.Add(48 * time.Hour)
	if snapshot = g.SourceClients(); len(snapshot.Clients) != 1 {
		t.Fatal("inactive records did not expire")
	}
	active.ActiveConnections = 0
	if snapshot = g.SourceClients(); len(snapshot.Clients) != 0 {
		t.Fatal("expired active record retained after release")
	}
}
