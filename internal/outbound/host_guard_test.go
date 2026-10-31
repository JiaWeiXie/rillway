package outbound

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"rillway/internal/hostlocal"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDirectGuardRefusesResolvedSocketBeforeConnect(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	var calls atomic.Int32
	ctx := WithResolvedGuard(t.Context(), func(network string, dst netip.AddrPort) error {
		calls.Add(1)
		if network != "tcp4" || !dst.Addr().IsLoopback() || dst.Port() == 0 {
			t.Error("guard received unresolved/non-numeric candidate", network, dst)
		}
		return hostlocal.ErrDenied
	})
	c, err := (&direct{id: "direct-alias"}).DialContext(ctx, "tcp4", "localhost:"+port)
	if c != nil {
		_ = c.Close()
		t.Fatal("denied direct connection returned")
	}
	if !errors.Is(err, hostlocal.ErrDenied) || calls.Load() == 0 {
		t.Fatal("numeric guard not applied/preserved", calls.Load(), err)
	}
	if err := l.(*net.TCPListener).SetDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if accepted, err := l.Accept(); err == nil {
		_ = accepted.Close()
		t.Fatal("refusal happened after connect")
	}
}

func TestDirectGuardIsContextScopedAndPreservesVerifiedIdentity(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	var calls atomic.Int32
	ctx := WithResolvedGuard(t.Context(), func(network string, dst netip.AddrPort) error {
		calls.Add(1)
		if !strings.HasPrefix(network, "tcp") || dst.String() != l.Addr().String() {
			t.Error("unexpected guard candidate", network, dst)
		}
		return nil
	})
	for _, dialCtx := range []context.Context{ctx, t.Context()} {
		c, err := (&direct{id: "alias"}).DialContext(dialCtx, "tcp4", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		if c.(interface{ DestinationIP() string }).DestinationIP() != "127.0.0.1" {
			t.Fatal("lost trusted direct identity")
		}
		_ = c.Close()
		peer, err := l.Accept()
		if err != nil {
			t.Fatal(err)
		}
		_ = peer.Close()
	}
	if calls.Load() != 1 {
		t.Fatal("guard leaked to unrelated context", calls.Load())
	}
}

func TestDirectUDPGuardAndCancellation(t *testing.T) {
	var calls atomic.Int32
	ctx := WithResolvedGuard(t.Context(), func(network string, dst netip.AddrPort) error {
		calls.Add(1)
		if network != "udp4" || !dst.Addr().IsLoopback() {
			t.Error("wrong UDP candidate", network, dst)
		}
		return hostlocal.ErrDenied
	})
	c, err := (&direct{id: "direct"}).DialContext(ctx, "udp4", "127.0.0.1:3000")
	if c != nil {
		_ = c.Close()
		t.Fatal("UDP guard returned connection")
	}
	if !errors.Is(err, hostlocal.ErrDenied) || calls.Load() != 1 {
		t.Fatal(calls.Load(), err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = (&direct{id: "direct"}).DialContext(cancelled, "tcp4", "127.0.0.1:3000")
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatal("cancelled guard dial continued", calls.Load(), err)
	}
}
