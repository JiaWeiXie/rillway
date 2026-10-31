package proxy

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"rillway/internal/hostlocal"
	"rillway/internal/routeinfo"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type hostUDPWriter struct {
	net.Conn
	writes  atomic.Int32
	enter   chan struct{}
	release chan struct{}
}

func (c *hostUDPWriter) Write(payload []byte) (int, error) {
	if c.enter != nil {
		c.enter <- struct{}{}
		<-c.release
	}
	c.writes.Add(1)
	return c.Conn.Write(payload)
}

func cachedHostUDP(t *testing.T) (*socksUDPAssociation, *socksUDPTarget, *hostUDPWriter, *hostlocal.State) {
	t.Helper()
	s := testServer(t, false)
	p, err := hostlocal.Compile(hostlocal.Open, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := hostlocal.NewState(p)
	s.SetHostLocalPolicy(state)
	left, right := net.Pipe()
	writer := &hostUDPWriter{Conn: left}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = io.Copy(io.Discard, right) }()
	t.Cleanup(func() { _ = left.Close(); _ = right.Close(); <-done })
	if !s.track(writer) || !s.udp.acquire() {
		t.Fatal("fixture target not tracked")
	}
	target := &socksUDPTarget{address: "alias.test:3000", conn: writer, hostDestination: netip.MustParseAddrPort("127.0.0.1:3000")}
	source := netip.MustParseAddr("192.0.2.2")
	ctx := routeinfo.With(t.Context(), routeinfo.Info{SourceIP: source, Inbound: "socks5"})
	a := &socksUDPAssociation{server: s, ctx: ctx, sourceIP: source, idleTimeout: time.Second, targets: map[string]*socksUDPTarget{target.address: target}}
	return a, target, writer, state
}

func writeCachedHostUDP(a *socksUDPAssociation, target *socksUDPTarget) (bool, error) {
	ctx, event := a.server.beginHostEvent(a.ctx, target.address)
	written, err := a.writeHostTarget(ctx, target, []byte("synthetic-datagram"))
	event.Complete(err)
	return written, err
}

func TestHostLocalCachedUDPAliasChecksCurrentPolicyPerDatagram(t *testing.T) {
	a, target, writer, state := cachedHostUDP(t)
	for range 2 {
		if written, err := writeCachedHostUDP(a, target); !written || err != nil {
			t.Fatal(written, err)
		}
	}
	if got := state.Snapshot(); got.ActualDenials != 0 || got.HypotheticalLocalClientsOnlyDenials != 2 {
		t.Fatal("cached alias not counted per packet", got)
	}
	p, err := hostlocal.Compile(hostlocal.LocalClientsOnly, []hostlocal.RuleSpec{{IP: target.hostDestination.Addr(), Lo: 3000, Hi: 3000, From: []string{"192.0.2.0/24"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state.Publish(p)
	if written, err := writeCachedHostUDP(a, target); !written || err != nil {
		t.Fatal("matching exception lost cached UDP", written, err)
	}
	p, err = hostlocal.Compile(hostlocal.LocalClientsOnly, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	state.Publish(p)
	if written, err := writeCachedHostUDP(a, target); written || !errors.Is(err, hostlocal.ErrDenied) {
		t.Fatal("cached forbidden payload escaped", written, err)
	}
	if writer.writes.Load() != 3 || a.server.udp.targets.Load() != 0 || len(a.targets) != 0 {
		t.Fatal("forbidden write or target budget leak", writer.writes.Load(), a.server.udp.targets.Load())
	}
	a.forget(target)
	if a.server.udp.targets.Load() != 0 {
		t.Fatal("target budget released twice")
	}
	got := state.Snapshot()
	if got.ActualDenials != 1 || got.HypotheticalLocalClientsOnlyDenials != 3 {
		t.Fatal(got)
	}
	// The pinned client port is validated before creating any policy event.
	a.client = netip.AddrPortFrom(a.sourceIP, 1000)
	a.forward(1001, target.address, nil, []byte("spoofed"))
	if next := state.Snapshot(); next.ActualDenials != got.ActualDenials {
		t.Fatal("unaccepted port counted as a policy operation")
	}
}

func TestHostLocalPublicationDoesNotRevokeAuthorizedUDPWrite(t *testing.T) {
	a, target, writer, state := cachedHostUDP(t)
	writer.enter, writer.release = make(chan struct{}, 1), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(writer.release) }) })
	result := make(chan error, 1)
	go func() {
		written, err := writeCachedHostUDP(a, target)
		if !written && err == nil {
			err = errors.New("authorized write lost")
		}
		result <- err
	}()
	select {
	case <-writer.enter:
	case <-time.After(time.Second):
		t.Fatal("write did not reach authorization boundary")
	}
	p, err := hostlocal.Compile(hostlocal.Strict, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	state.Publish(p)
	release.Do(func() { close(writer.release) })
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("publication blocked authorized write")
	}
	if written, err := writeCachedHostUDP(a, target); written || !errors.Is(err, hostlocal.ErrDenied) {
		t.Fatal("later check used old policy", written, err)
	}
	if writer.writes.Load() != 1 {
		t.Fatal("later forbidden send reached socket")
	}
}
