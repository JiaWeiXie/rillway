package proxy

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestRelayClosesBothSidesOnReset(t *testing.T) {
	clientPeer, clientSide := tcpPair(t)
	defer func() { _ = clientSide.Close() }()
	upstreamSide, upstreamPeer := tcpPair(t)
	defer func() { _ = upstreamSide.Close(); _ = upstreamPeer.Close() }()
	done := make(chan struct{})
	go func() { relay(context.Background(), clientSide, upstreamSide); close(done) }()
	if err := clientPeer.(*net.TCPConn).SetLinger(0); err != nil {
		t.Fatal(err)
	}
	if err := clientPeer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not return after reset")
	}
	if err := upstreamPeer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := upstreamPeer.Read(b[:]); err == nil {
		t.Fatal("upstream peer remained open")
	}
}

func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	peer, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case side := <-accepted:
		return peer, side
	case <-time.After(time.Second):
		t.Fatal("accept timed out")
		return nil, nil
	}
}
