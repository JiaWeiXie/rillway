package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync"
	"testing"
	"time"
)

type aliasDialer struct{ target string }

func (d aliasDialer) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, d.target)
}

func TestAliasCheckedUsingSelectedOutboundPeer(t *testing.T) {
	s := testServer(t, false)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	_ = s.GuardListener(l)
	s.dialer = aliasDialer{target: l.Addr().String()}
	c, err := s.dialContext(t.Context(), "tcp", "alias.invalid:80")
	if err == nil {
		_ = c.Close()
		t.Fatal("resolved self endpoint accepted")
	}
	// The endpoint is known only after provider resolution; no host lookup of
	// the reserved alias is needed to reject it.
	peer, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = peer.Read(make([]byte, 1)); err != io.EOF {
		t.Fatal("self peer was not closed before sending payload", err)
	}
}

func TestOwnEndpointWildcardMappedIPv4AndRemoteHost(t *testing.T) {
	s := testServer(t, false)
	s.endpoints = []netip.AddrPort{netip.MustParseAddrPort("0.0.0.0:17890")}
	for _, address := range []string{"127.0.0.1:17890", "127.0.0.2:17890", "[::ffff:127.0.0.1]:17890"} {
		if !s.ownEndpoint(address) {
			t.Fatal("local endpoint not identified", address)
		}
	}
	if s.ownEndpoint("192.0.2.20:17890") || s.ownEndpoint("127.0.0.1:17893") {
		t.Fatal("unrelated host or PAC port blocked")
	}
}

func TestInternalPolicyMatchesOnlyOwnBoundServices(t *testing.T) {
	s := testServer(t, false)
	s.endpoints = []netip.AddrPort{netip.MustParseAddrPort("0.0.0.0:17890")}
	s.internalEndpoints = []netip.AddrPort{netip.MustParseAddrPort("0.0.0.0:17893"), netip.MustParseAddrPort("[::1]:17892")}
	for _, address := range []string{"127.0.0.1:17893", "[::ffff:127.0.0.1]:17893", "[::1]:17892"} {
		blocked, internal := s.DestinationPolicy(address)
		if blocked || !internal {
			t.Fatal("own management/PAC endpoint was not ignored", address)
		}
	}
	for _, address := range []string{"127.0.0.1:8080", "192.0.2.20:17893", "[::1]:17893", "[::1]:17890"} {
		blocked, internal := s.DestinationPolicy(address)
		if blocked || internal {
			t.Fatal("unrelated endpoint was ignored or blocked", address)
		}
	}
	if blocked, internal := s.DestinationPolicy("127.0.0.1:17890"); !blocked || internal {
		t.Fatal("own proxy endpoint lost loop protection")
	}
}

func TestGuardRejectsBeforeProtocolAndRecoversOnClose(t *testing.T) {
	for _, limits := range [][2]int{{1, 10}, {10, 1}} {
		t.Run(fmt.Sprint(limits), func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = l.Close() }()
			g := NewConnectionGuard(limits[0], limits[1], nil)
			guarded := g.Wrap(l)
			accepted := make(chan net.Conn, 2)
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				for {
					c, err := guarded.Accept()
					if err != nil {
						return
					}
					accepted <- c
				}
			}()
			defer func() { _ = l.Close(); <-stopped }()
			dial := func() net.Conn {
				c, err := net.DialTimeout("tcp", l.Addr().String(), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.Close() })
				return c
			}
			_ = dial()
			var first net.Conn
			select {
			case first = <-accepted:
			case <-time.After(time.Second):
				t.Fatal("first connection was not accepted")
			}
			defer func() { _ = first.Close() }()
			rejected := dial()
			_ = rejected.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := rejected.Read(make([]byte, 1)); err == nil {
				t.Fatal("excess connection was not closed")
			} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("excess connection was left waiting")
			}
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() { _ = first.Close() })
			}
			wg.Wait()
			_ = dial()
			select {
			case next := <-accepted:
				_ = next.Close()
			case <-time.After(time.Second):
				t.Fatal("connection budget was not released")
			}
			g.mu.Lock()
			defer g.mu.Unlock()
			if g.active != 0 || len(g.clients) != 0 {
				t.Fatal("connection/source counters leaked")
			}
		})
	}
}

func TestHTTPAndCONNECTRejectSelfAndDNSAlias(t *testing.T) {
	s := testServer(t, false)
	front := httptest.NewUnstartedServer(s.HTTPHandler())
	front.Listener = s.GuardListener(front.Listener)
	front.Start()
	defer front.Close()
	proxyURL, _ := url.Parse(front.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	_, port, _ := net.SplitHostPort(front.Listener.Addr().String())
	for _, host := range []string{"127.0.0.1", "localhost"} {
		for _, scheme := range []string{"http", "https"} {
			response, err := client.Get(scheme + "://" + net.JoinHostPort(host, port))
			if scheme == "https" {
				if err == nil {
					_ = response.Body.Close()
					t.Fatal("self CONNECT accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusBadGateway {
					t.Fatal("self HTTP not rejected", response.StatusCode)
				}
			}
		}
	}
	// Non-proxy local services remain accessible (including the PAC listener).
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer origin.Close()
	response, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatal("ordinary local origin blocked")
	}
}

func TestGuardedCONNECTPreservesHalfClose(t *testing.T) {
	origin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = origin.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := origin.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		body, _ := io.ReadAll(c)
		_, _ = c.Write(body)
	}()
	s := testServer(t, false)
	front := httptest.NewUnstartedServer(s.HTTPHandler())
	front.Listener = s.GuardListener(front.Listener)
	front.Start()
	defer front.Close()
	c, err := net.DialTimeout("tcp", front.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	_, err = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", origin.Addr(), origin.Addr())
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(c)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatal(response, err)
	}
	_, _ = io.WriteString(c, "hello")
	_ = c.(*net.TCPConn).CloseWrite()
	body, err := io.ReadAll(reader)
	if err != nil || string(body) != "hello" {
		t.Fatal("half-close lost", string(body), err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("origin did not close")
	}
}

func TestSOCKSSelfDestinationIsRejected(t *testing.T) {
	s := testServer(t, false)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l = s.GuardListener(l)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.ServeSOCKS(ctx, l) }()
	defer func() { _ = s.Close(); <-done }()
	c, err := net.DialTimeout("tcp", l.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	_, _ = c.Write([]byte{5, 1, 0})
	var greeting [2]byte
	if _, err = io.ReadFull(c, greeting[:]); err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_, _ = c.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, byte(port >> 8), byte(port)})
	var reply [10]byte
	if _, err = io.ReadFull(c, reply[:]); err != nil || reply[1] != 5 {
		t.Fatal("self SOCKS not rejected", reply, err)
	}
}
