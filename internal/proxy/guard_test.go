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
	"rillway/internal/access"
	"rillway/internal/config"
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
		totalLimited := limits[0] < limits[1]
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
			want := AdmissionRejections{SourceLimit: 1}
			if totalLimited {
				want = AdmissionRejections{TotalLimit: 1}
			}
			if got := g.Rejections(); got != want {
				t.Fatalf("rejections = %+v, want %+v", got, want)
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

type fakeConn struct {
	net.Conn
	remote   net.Addr
	closed   sync.Once
	isClosed bool
}

func (c *fakeConn) RemoteAddr() net.Addr { return c.remote }
func (c *fakeConn) Close() error {
	c.closed.Do(func() {
		c.isClosed = true
	})
	return nil
}
func (c *fakeConn) Read(b []byte) (int, error)  { return 0, io.EOF }
func (c *fakeConn) Write(b []byte) (int, error) { return len(b), nil }

type fakeListener struct {
	conns  chan net.Conn
	addr   net.Addr
	closed sync.Once
}

func newFakeListener(addr string) *fakeListener {
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		tcpAddr = &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 17890}
	}
	return &fakeListener{
		conns: make(chan net.Conn, 2048),
		addr:  tcpAddr,
	}
}

func (l *fakeListener) Accept() (net.Conn, error) {
	c, ok := <-l.conns
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}

func (l *fakeListener) Close() error {
	l.closed.Do(func() {
		close(l.conns)
	})
	return nil
}

func (l *fakeListener) Addr() net.Addr { return l.addr }

func (l *fakeListener) enqueue(ip string) *fakeConn {
	c := &fakeConn{
		remote: &net.TCPAddr{IP: net.ParseIP(ip), Port: 12345},
	}
	l.conns <- c
	return c
}

func TestSharedTwoWrappedListeners(t *testing.T) {
	g := NewConnectionGuard(3, 2, nil)
	httpL := newFakeListener("127.0.0.1:17890")
	socksL := newFakeListener("127.0.0.1:17891")
	defer func() { _ = httpL.Close(); _ = socksL.Close() }()

	wHTTP := g.Wrap(httpL)
	wSOCKS := g.Wrap(socksL)

	// Conn 1 from 192.0.2.1 on HTTP -> accepted
	httpL.enqueue("192.0.2.1")
	c1, err := wHTTP.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c1.Close() }()

	// Conn 2 from 192.0.2.1 on SOCKS -> accepted
	socksL.enqueue("192.0.2.1")
	c2, err := wSOCKS.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c2.Close() }()

	// Conn 3 from 192.0.2.1 on HTTP -> rejected (per-source limit 2 reached)
	raw3 := httpL.enqueue("192.0.2.1")
	// Enqueue an accepted conn from 192.0.2.2 so wHTTP.Accept returns
	httpL.enqueue("192.0.2.2")
	c3, err := wHTTP.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c3.Close() }()

	if !raw3.isClosed {
		t.Fatal("conn 3 was not closed")
	}
	if g.Rejections().SourceLimit != 1 {
		t.Fatalf("expected 1 source limit rejection, got %d", g.Rejections().SourceLimit)
	}

	// Total active is 3. Total limit reached!
	// Conn 4 from 192.0.2.3 on SOCKS -> rejected (total limit reached)
	lClose := newFakeListener("127.0.0.1:17891")
	wSOCKS2 := g.Wrap(lClose)
	raw4 := lClose.enqueue("192.0.2.3")
	_ = lClose.Close()
	_, _ = wSOCKS2.Accept()

	if !raw4.isClosed {
		t.Fatal("conn 4 was not closed")
	}
	if g.Rejections().TotalLimit != 1 {
		t.Fatalf("expected 1 total limit rejection, got %d", g.Rejections().TotalLimit)
	}

	// Release c1: active drops to 2. SOCKS accepts new conn!
	_ = c1.Close()
	lNew := newFakeListener("127.0.0.1:17891")
	wSOCKS3 := g.Wrap(lNew)
	defer func() { _ = lNew.Close() }()
	lNew.enqueue("192.0.2.3")
	c5, err := wSOCKS3.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c5.Close() }()
	if g.Capacity().ActiveConnections != 3 {
		t.Fatalf("expected 3 active connections, got %d", g.Capacity().ActiveConnections)
	}
}

func TestTotalAndSourcePrecedenceAndRejections(t *testing.T) {
	// Case 1: Both limits full -> TotalLimit wins!
	{
		g := NewConnectionGuard(2, 2, nil)
		l := newFakeListener("127.0.0.1:17890")
		gl := g.Wrap(l)
		l.enqueue("192.0.2.1")
		c1, _ := gl.Accept()
		defer func() { _ = c1.Close() }()
		l.enqueue("192.0.2.1")
		c2, _ := gl.Accept()
		defer func() { _ = c2.Close() }()

		// Active=2 (>=total 2) AND ip=2 (>=perIP 2)
		raw := l.enqueue("192.0.2.1")
		_ = l.Close()
		_, _ = gl.Accept()

		if !raw.isClosed {
			t.Fatal("connection was not closed")
		}
		if g.Rejections().TotalLimit != 1 || g.Rejections().SourceLimit != 0 {
			t.Fatalf("expected TotalLimit=1, SourceLimit=0 when both full; got %+v", g.Rejections())
		}
		snap := g.SourceClients()
		if len(snap.Clients) != 1 {
			t.Fatalf("expected 1 client, got %d", len(snap.Clients))
		}
		client := snap.Clients[0]
		if client.CapacityRejections != 1 || client.TotalLimitRejections != 1 || client.SourceLimitRejections != 0 {
			t.Fatalf("client rejection breakdown mismatch: %+v", client)
		}
	}

	// Case 2: Source limit full, total limit NOT full -> SourceLimit wins
	{
		g := NewConnectionGuard(10, 1, nil)
		l := newFakeListener("127.0.0.1:17890")
		gl := g.Wrap(l)
		l.enqueue("192.0.2.2")
		c1, _ := gl.Accept()
		defer func() { _ = c1.Close() }()

		raw := l.enqueue("192.0.2.2")
		_ = l.Close()
		_, _ = gl.Accept()

		if !raw.isClosed {
			t.Fatal("connection was not closed")
		}
		if g.Rejections().SourceLimit != 1 || g.Rejections().TotalLimit != 0 {
			t.Fatalf("expected SourceLimit=1, TotalLimit=0; got %+v", g.Rejections())
		}
		snap := g.SourceClients()
		client := snap.Clients[0]
		if client.CapacityRejections != 1 || client.SourceLimitRejections != 1 || client.TotalLimitRejections != 0 {
			t.Fatalf("client rejection breakdown mismatch: %+v", client)
		}
	}

	// Case 3: Total limit full, source limit NOT full -> TotalLimit wins
	{
		g := NewConnectionGuard(2, 2, nil)
		l := newFakeListener("127.0.0.1:17890")
		gl := g.Wrap(l)
		l.enqueue("192.0.2.3")
		c1, _ := gl.Accept()
		defer func() { _ = c1.Close() }()
		l.enqueue("192.0.2.4")
		c2, _ := gl.Accept()
		defer func() { _ = c2.Close() }()

		// 192.0.2.5 has 0 conns (< perIP 2), but active=2 (>= total 2)
		raw := l.enqueue("192.0.2.5")
		_ = l.Close()
		_, _ = gl.Accept()

		if !raw.isClosed {
			t.Fatal("connection was not closed")
		}
		if g.Rejections().TotalLimit != 1 || g.Rejections().SourceLimit != 0 {
			t.Fatalf("expected TotalLimit=1, SourceLimit=0; got %+v", g.Rejections())
		}
		snap := g.SourceClients()
		var client5 *access.SourceClient
		for i := range snap.Clients {
			if snap.Clients[i].Address == "192.0.2.5" {
				client5 = &snap.Clients[i]
				break
			}
		}
		if client5 == nil {
			t.Fatal("missing client 192.0.2.5 in snapshot")
		}
		if client5.CapacityRejections != 1 || client5.TotalLimitRejections != 1 || client5.SourceLimitRejections != 0 {
			t.Fatalf("client rejection breakdown mismatch: %+v", client5)
		}
	}
}

func TestPolicyDenialZeroCapacityConfusion(t *testing.T) {
	policy, err := access.Compile([]access.RuleSpec{
		{ID: "deny-rule", Action: "deny", CIDRs: []string{"192.0.2.99/32"}, Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	g := NewConnectionGuard(0, 0, policy.Decide)
	l := newFakeListener("127.0.0.1:17890")
	gl := g.Wrap(l)

	raw := l.enqueue("192.0.2.99")
	_ = l.Close()
	_, _ = gl.Accept()

	if !raw.isClosed {
		t.Fatal("denied conn was not closed")
	}
	// Policy denial wins before capacity!
	if g.Rejections().TotalLimit != 0 || g.Rejections().SourceLimit != 0 {
		t.Fatalf("rejections should be 0, got Total=%d Source=%d", g.Rejections().TotalLimit, g.Rejections().SourceLimit)
	}
	snap := g.SourceClients()
	if len(snap.Clients) != 1 {
		t.Fatalf("expected 1 client in telemetry, got %d", len(snap.Clients))
	}
	row := snap.Clients[0]
	if row.DeniedConnections != 1 {
		t.Fatalf("expected DeniedConnections=1, got %d", row.DeniedConnections)
	}
	if row.CapacityRejections != 0 || row.SourceLimitRejections != 0 || row.TotalLimitRejections != 0 {
		t.Fatalf("capacity rejections should be 0 for policy denial, got %+v", row)
	}
}

func TestRepeatedAndConcurrentCloseCountsRecovery(t *testing.T) {
	g := NewConnectionGuard(100, 100, nil)
	l := newFakeListener("127.0.0.1:17890")
	gl := g.Wrap(l)
	defer func() { _ = l.Close() }()

	conns := make([]net.Conn, 50)
	for i := range 50 {
		ip := fmt.Sprintf("192.0.2.%d", (i%10)+1)
		l.enqueue(ip)
		c, err := gl.Accept()
		if err != nil {
			t.Fatal(err)
		}
		conns[i] = c
	}

	if cap := g.Capacity(); cap.ActiveConnections != 50 {
		t.Fatalf("expected 50 active connections, got %d", cap.ActiveConnections)
	}

	var wg sync.WaitGroup
	for _, c := range conns {
		conn := c
		for range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = conn.Close()
			}()
		}
	}
	wg.Wait()

	if cap := g.Capacity(); cap.ActiveConnections != 0 {
		t.Fatalf("expected 0 active connections after concurrent closes, got %d", cap.ActiveConnections)
	}
	g.mu.Lock()
	active := g.active
	clientCount := len(g.clients)
	ingressCount := len(g.ingress)
	g.mu.Unlock()
	if active != 0 || clientCount != 0 || ingressCount != 0 {
		t.Fatalf("state leaked: active=%d, clients=%d, ingress=%d", active, clientCount, ingressCount)
	}
}

func TestActiveClientsBeyond1024(t *testing.T) {
	const count = 1200
	g := NewConnectionGuard(1500, 1, nil)
	l := newFakeListener("127.0.0.1:17890")
	gl := g.Wrap(l)
	defer func() { _ = l.Close() }()

	conns := make([]net.Conn, count)
	for i := range count {
		ip := fmt.Sprintf("10.0.%d.%d", i/256, i%256)
		l.enqueue(ip)
		c, err := gl.Accept()
		if err != nil {
			t.Fatalf("failed to accept conn %d: %v", i, err)
		}
		conns[i] = c
	}

	cap := g.Capacity()
	if cap.ActiveConnections != count {
		t.Fatalf("expected %d active, got %d", count, cap.ActiveConnections)
	}
	if cap.SourceLimit != 0 || cap.TotalLimit != 0 {
		t.Fatalf("unexpected rejections: Source=%d, Total=%d", cap.SourceLimit, cap.TotalLimit)
	}

	snap := g.SourceClients()
	if snap.Capacity.ActiveConnections != count {
		t.Fatalf("snapshot capacity active = %d, want %d", snap.Capacity.ActiveConnections, count)
	}
	if len(snap.Clients) != count {
		t.Fatalf("snapshot clients count = %d, want %d", len(snap.Clients), count)
	}

	for _, c := range conns {
		_ = c.Close()
	}

	if cap := g.Capacity(); cap.ActiveConnections != 0 {
		t.Fatalf("expected 0 active after close, got %d", cap.ActiveConnections)
	}
}

func TestApplyConnectionLimitsLowerActiveBelow(t *testing.T) {
	g := NewConnectionGuard(10, 5, nil)
	l := newFakeListener("127.0.0.1:17890")
	gl := g.Wrap(l)
	defer func() { _ = l.Close() }()

	// Connect 3: 2 from 10.0.0.1, 1 from 10.0.0.2
	l.enqueue("10.0.0.1")
	c1, err := gl.Accept()
	if err != nil {
		t.Fatalf("c1 accept: %v", err)
	}
	defer func() { _ = c1.Close() }()

	l.enqueue("10.0.0.1")
	c2, err := gl.Accept()
	if err != nil {
		t.Fatalf("c2 accept: %v", err)
	}
	defer func() { _ = c2.Close() }()

	l.enqueue("10.0.0.2")
	c3, err := gl.Accept()
	if err != nil {
		t.Fatalf("c3 accept: %v", err)
	}
	defer func() { _ = c3.Close() }()

	if cap := g.Capacity(); cap.ActiveConnections != 3 {
		t.Fatalf("expected 3 active, got %d", cap.ActiveConnections)
	}

	// Lower limits to 5 total, 3 per source. Active (3) is below new total (5),
	// and per-source (2 and 1) are below new per-source (3).
	g.ApplyConnectionLimits(config.ConnectionLimits{MaxConnections: 5, MaxConnectionsPerSource: 3})

	cap := g.Capacity()
	if cap.MaxConnections != 5 || cap.MaxConnectionsPerSource != 3 {
		t.Fatalf("unexpected limits after apply: %+v", cap)
	}
	if cap.ActiveConnections != 3 {
		t.Fatalf("expected 3 active after lower, got %d", cap.ActiveConnections)
	}

	// Verify old streams untouched (not closed)
	for idx, c := range []*fakeConn{c1.(*guardedConn).Conn.(*fakeConn), c2.(*guardedConn).Conn.(*fakeConn), c3.(*guardedConn).Conn.(*fakeConn)} {
		if c.isClosed {
			t.Fatalf("old stream %d was closed", idx)
		}
	}

	// New connection from 10.0.0.2 (now has 2, within limit 3; total 4, within limit 5)
	l.enqueue("10.0.0.2")
	c4, err := gl.Accept()
	if err != nil {
		t.Fatalf("c4 accept: %v", err)
	}
	defer func() { _ = c4.Close() }()

	if cap := g.Capacity(); cap.ActiveConnections != 4 {
		t.Fatalf("expected 4 active after new conn, got %d", cap.ActiveConnections)
	}
	if cap.SourceLimit != 0 || cap.TotalLimit != 0 {
		t.Fatalf("unexpected rejections: %+v", cap)
	}
}

func TestApplyConnectionLimitsLowerActiveAboveTotal(t *testing.T) {
	g := NewConnectionGuard(10, 5, nil)
	l := newFakeListener("127.0.0.1:17890")
	gl := g.Wrap(l)
	defer func() { _ = l.Close() }()

	// Connect 6 across 6 distinct IPs
	conns := make([]net.Conn, 6)
	for i := range 6 {
		ip := fmt.Sprintf("10.0.0.%d", i+1)
		l.enqueue(ip)
		c, err := gl.Accept()
		if err != nil {
			t.Fatalf("accept %d: %v", i, err)
		}
		conns[i] = c
	}

	// Lower total limit to 3 (active 6 > 3)
	g.ApplyConnectionLimits(config.ConnectionLimits{MaxConnections: 3, MaxConnectionsPerSource: 2})

	// Existing connections MUST NOT be closed
	for i, c := range conns {
		fc := c.(*guardedConn).Conn.(*fakeConn)
		if fc.isClosed {
			t.Fatalf("connection %d closed prematurely", i)
		}
	}
	if cap := g.Capacity(); cap.ActiveConnections != 6 {
		t.Fatalf("expected 6 active, got %d", cap.ActiveConnections)
	}

	// New connection should be rejected due to total limit
	raw1 := l.enqueue("10.0.0.99")
	_ = l.Close()
	_, _ = gl.Accept()

	if !raw1.isClosed {
		t.Fatal("expected new connection to be rejected when active > total")
	}
	if rej := g.Rejections(); rej.TotalLimit != 1 || rej.SourceLimit != 0 {
		t.Fatalf("expected TotalLimit=1, SourceLimit=0; got %+v", rej)
	}

	// Close 3 connections -> active=3 (still >= total 3)
	for i := range 3 {
		_ = conns[i].Close()
	}
	if cap := g.Capacity(); cap.ActiveConnections != 3 {
		t.Fatalf("expected 3 active, got %d", cap.ActiveConnections)
	}

	// Rejection should still occur at active=3 (>= total 3)
	l2 := newFakeListener("127.0.0.1:17890")
	gl2 := g.Wrap(l2)
	raw2 := l2.enqueue("10.0.0.99")
	_ = l2.Close()
	_, _ = gl2.Accept()

	if !raw2.isClosed {
		t.Fatal("expected rejection when active == total")
	}
	if rej := g.Rejections(); rej.TotalLimit != 2 || rej.SourceLimit != 0 {
		t.Fatalf("expected TotalLimit=2, SourceLimit=0; got %+v", rej)
	}

	// Close 1 more -> active=2 (< total 3)
	_ = conns[3].Close()
	if cap := g.Capacity(); cap.ActiveConnections != 2 {
		t.Fatalf("expected 2 active, got %d", cap.ActiveConnections)
	}

	// New connection can now be admitted!
	l3 := newFakeListener("127.0.0.1:17890")
	gl3 := g.Wrap(l3)
	defer func() { _ = l3.Close() }()
	l3.enqueue("10.0.0.99")
	cAccepted, err := gl3.Accept()
	if err != nil {
		t.Fatalf("expected admission after budget recovered: %v", err)
	}
	defer func() { _ = cAccepted.Close() }()

	if cap := g.Capacity(); cap.ActiveConnections != 3 {
		t.Fatalf("expected 3 active after admission, got %d", cap.ActiveConnections)
	}
	// Cumulative counters must survive
	if rej := g.Rejections(); rej.TotalLimit != 2 || rej.SourceLimit != 0 {
		t.Fatalf("expected TotalLimit=2 preserved, got %+v", rej)
	}

	// Clean up remaining
	_ = conns[4].Close()
	_ = conns[5].Close()
}

func TestApplyConnectionLimitsLowerActiveAboveSource(t *testing.T) {
	g := NewConnectionGuard(20, 10, nil)
	l := newFakeListener("127.0.0.1:17890")
	gl := g.Wrap(l)
	defer func() { _ = l.Close() }()

	// Connect 5 from 10.0.0.1 and 2 from 10.0.0.2
	conns1 := make([]net.Conn, 5)
	for i := range 5 {
		l.enqueue("10.0.0.1")
		c, err := gl.Accept()
		if err != nil {
			t.Fatalf("accept 10.0.0.1 #%d: %v", i, err)
		}
		conns1[i] = c
	}
	conns2 := make([]net.Conn, 2)
	for i := range 2 {
		l.enqueue("10.0.0.2")
		c, err := gl.Accept()
		if err != nil {
			t.Fatalf("accept 10.0.0.2 #%d: %v", i, err)
		}
		conns2[i] = c
	}

	// Lower per-source limit to 3 (total 15, active 7 < 15, but 10.0.0.1 active 5 > 3)
	g.ApplyConnectionLimits(config.ConnectionLimits{MaxConnections: 15, MaxConnectionsPerSource: 3})

	// Existing connections untouched
	for _, c := range append(conns1, conns2...) {
		fc := c.(*guardedConn).Conn.(*fakeConn)
		if fc.isClosed {
			t.Fatal("existing connection closed on limit decrease")
		}
	}

	// New connection from 10.0.0.1 rejected (SourceLimit)
	raw1 := l.enqueue("10.0.0.1")
	_ = l.Close()
	_, _ = gl.Accept()

	if !raw1.isClosed {
		t.Fatal("expected 10.0.0.1 to be rejected when source active > perIP")
	}
	if rej := g.Rejections(); rej.SourceLimit != 1 || rej.TotalLimit != 0 {
		t.Fatalf("expected SourceLimit=1, TotalLimit=0; got %+v", rej)
	}

	// But new connection from 10.0.0.2 (active 2 < 3, total 7 < 15) is admitted!
	l2 := newFakeListener("127.0.0.1:17890")
	gl2 := g.Wrap(l2)
	l2.enqueue("10.0.0.2")
	cAccepted, err := gl2.Accept()
	if err != nil {
		t.Fatalf("expected 10.0.0.2 to be admitted: %v", err)
	}
	defer func() { _ = cAccepted.Close() }()

	// Now 10.0.0.2 has 3 active (= limit 3). Next connection from 10.0.0.2 should be rejected.
	raw2 := l2.enqueue("10.0.0.2")
	_ = l2.Close()
	_, _ = gl2.Accept()

	if !raw2.isClosed {
		t.Fatal("expected 10.0.0.2 to be rejected when source active == perIP")
	}
	if rej := g.Rejections(); rej.SourceLimit != 2 || rej.TotalLimit != 0 {
		t.Fatalf("expected SourceLimit=2, TotalLimit=0; got %+v", rej)
	}

	// Close 3 connections from 10.0.0.1 -> active 2 (< 3)
	for i := range 3 {
		_ = conns1[i].Close()
	}

	// 10.0.0.1 can now connect again!
	l3 := newFakeListener("127.0.0.1:17890")
	gl3 := g.Wrap(l3)
	defer func() { _ = l3.Close() }()
	l3.enqueue("10.0.0.1")
	cNew1, err := gl3.Accept()
	if err != nil {
		t.Fatalf("expected 10.0.0.1 to be admitted after closing: %v", err)
	}
	defer func() { _ = cNew1.Close() }()

	// Clean up
	_ = conns1[3].Close()
	_ = conns1[4].Close()
	for _, c := range conns2 {
		_ = c.Close()
	}
}

func TestApplyConnectionLimitsRejectUntilBothBudgetsRecovered(t *testing.T) {
	g := NewConnectionGuard(10, 10, nil)
	l := newFakeListener("127.0.0.1:17890")
	gl := g.Wrap(l)
	defer func() { _ = l.Close() }()

	// Connect 4 from 10.0.0.1
	conns := make([]net.Conn, 4)
	for i := range 4 {
		l.enqueue("10.0.0.1")
		c, err := gl.Accept()
		if err != nil {
			t.Fatalf("accept %d: %v", i, err)
		}
		conns[i] = c
	}

	// Lower limits to total=3, perSource=2
	// Active is 4 (exceeds total 3 AND perSource 2)
	g.ApplyConnectionLimits(config.ConnectionLimits{MaxConnections: 3, MaxConnectionsPerSource: 2})

	// Attempt from 10.0.0.1 -> TotalLimit wins when both exceeded
	raw1 := l.enqueue("10.0.0.1")
	_ = l.Close()
	_, _ = gl.Accept()

	if !raw1.isClosed {
		t.Fatal("expected rejection")
	}
	if rej := g.Rejections(); rej.TotalLimit != 1 || rej.SourceLimit != 0 {
		t.Fatalf("expected TotalLimit=1, got %+v", rej)
	}

	// Close 1: active=3 (total 3 >= 3, source 3 >= 2) -> still rejected by TotalLimit
	_ = conns[0].Close()
	l2 := newFakeListener("127.0.0.1:17890")
	gl2 := g.Wrap(l2)
	raw2 := l2.enqueue("10.0.0.1")
	_ = l2.Close()
	_, _ = gl2.Accept()

	if !raw2.isClosed {
		t.Fatal("expected rejection at total capacity")
	}
	if rej := g.Rejections(); rej.TotalLimit != 2 || rej.SourceLimit != 0 {
		t.Fatalf("expected TotalLimit=2, got %+v", rej)
	}

	// Close 2: active=2 (total 2 < 3, but source 2 >= 2) -> now rejected by SourceLimit!
	_ = conns[1].Close()
	l3 := newFakeListener("127.0.0.1:17890")
	gl3 := g.Wrap(l3)
	raw3 := l3.enqueue("10.0.0.1")
	_ = l3.Close()
	_, _ = gl3.Accept()

	if !raw3.isClosed {
		t.Fatal("expected rejection by SourceLimit")
	}
	if rej := g.Rejections(); rej.TotalLimit != 2 || rej.SourceLimit != 1 {
		t.Fatalf("expected TotalLimit=2, SourceLimit=1; got %+v", rej)
	}

	// Close 3: active=1 (total 1 < 3, source 1 < 2) -> BOTH budgets recovered!
	_ = conns[2].Close()
	l4 := newFakeListener("127.0.0.1:17890")
	gl4 := g.Wrap(l4)
	defer func() { _ = l4.Close() }()
	l4.enqueue("10.0.0.1")
	cAccepted, err := gl4.Accept()
	if err != nil {
		t.Fatalf("expected admission after both recovered: %v", err)
	}
	defer func() { _ = cAccepted.Close() }()

	if rej := g.Rejections(); rej.TotalLimit != 2 || rej.SourceLimit != 1 {
		t.Fatalf("cumulative counters must survive: %+v", rej)
	}

	_ = conns[3].Close()
}

func TestApplyConnectionLimitsRaiseAllowsImmediateAdmission(t *testing.T) {
	g := NewConnectionGuard(2, 2, nil)
	l := newFakeListener("127.0.0.1:17890")
	gl := g.Wrap(l)
	defer func() { _ = l.Close() }()

	l.enqueue("10.0.0.1")
	c1, _ := gl.Accept()
	defer func() { _ = c1.Close() }()
	l.enqueue("10.0.0.1")
	c2, _ := gl.Accept()
	defer func() { _ = c2.Close() }()

	// 3rd rejected
	raw := l.enqueue("10.0.0.1")
	_ = l.Close()
	_, _ = gl.Accept()
	if !raw.isClosed {
		t.Fatal("expected rejection")
	}
	if rej := g.Rejections(); rej.TotalLimit != 1 {
		t.Fatalf("expected TotalLimit=1, got %+v", rej)
	}

	// Raise limits immediately without closing existing
	g.ApplyConnectionLimits(config.ConnectionLimits{MaxConnections: 10, MaxConnectionsPerSource: 5})

	l2 := newFakeListener("127.0.0.1:17890")
	gl2 := g.Wrap(l2)
	defer func() { _ = l2.Close() }()
	l2.enqueue("10.0.0.1")
	c3, err := gl2.Accept()
	if err != nil {
		t.Fatalf("expected immediate admission after raise: %v", err)
	}
	defer func() { _ = c3.Close() }()

	if cap := g.Capacity(); cap.ActiveConnections != 3 {
		t.Fatalf("expected 3 active, got %d", cap.ActiveConnections)
	}
	if rej := g.Rejections(); rej.TotalLimit != 1 || rej.SourceLimit != 0 {
		t.Fatalf("counters survived: %+v", rej)
	}
}

func TestApplyConnectionLimitsSourceMetadataBoundLower(t *testing.T) {
	const count = 1200
	g := NewConnectionGuard(1500, 1, nil)
	l := newFakeListener("127.0.0.1:17890")
	gl := g.Wrap(l)
	defer func() { _ = l.Close() }()

	conns := make([]net.Conn, count)
	for i := range count {
		ip := fmt.Sprintf("10.1.%d.%d", i/256, i%256)
		l.enqueue(ip)
		c, err := gl.Accept()
		if err != nil {
			t.Fatalf("accept %d: %v", i, err)
		}
		conns[i] = c
	}

	// Close 200 connections -> 1000 active, 200 idle. Total in sources = 1200
	for i := range 200 {
		_ = conns[i].Close()
	}

	snap := g.SourceClients()
	if snap.Capacity.ActiveConnections != 1000 {
		t.Fatalf("expected 1000 active, got %d", snap.Capacity.ActiveConnections)
	}
	if len(snap.Clients) != 1200 {
		t.Fatalf("expected 1200 clients before limit lower, got %d", len(snap.Clients))
	}

	// Lower limits to Total=500. Bound becomes max(1024, 500) = 1024.
	g.ApplyConnectionLimits(config.ConnectionLimits{MaxConnections: 500, MaxConnectionsPerSource: 1})

	snapAfter := g.SourceClients()
	// All 1000 active rows must be preserved ("keep allactive rows")
	if snapAfter.Capacity.ActiveConnections != 1000 {
		t.Fatalf("expected 1000 active, got %d", snapAfter.Capacity.ActiveConnections)
	}
	// Idle rows evicted until within bounded budget: 1000 active + 24 idle = 1024
	if len(snapAfter.Clients) != 1024 {
		t.Fatalf("expected bounded oldbudget rows (1024), got %d", len(snapAfter.Clients))
	}
	activeCount := 0
	for _, cl := range snapAfter.Clients {
		if cl.ActiveConnections > 0 {
			activeCount++
		}
	}
	if activeCount != 1000 {
		t.Fatalf("expected all 1000 active clients retained, got %d", activeCount)
	}

	// Clean up remaining 1000
	for i := 200; i < count; i++ {
		_ = conns[i].Close()
	}
}

func TestApplyConnectionLimitsKeepAllActiveWhenExceedingNewBound(t *testing.T) {
	const count = 1100
	g := NewConnectionGuard(1500, 1, nil)
	l := newFakeListener("127.0.0.1:17890")
	gl := g.Wrap(l)
	defer func() { _ = l.Close() }()

	conns := make([]net.Conn, count)
	for i := range count {
		ip := fmt.Sprintf("10.2.%d.%d", i/256, i%256)
		l.enqueue(ip)
		c, err := gl.Accept()
		if err != nil {
			t.Fatalf("accept %d: %v", i, err)
		}
		conns[i] = c
	}

	// Lower limits to Total=256 (bound = max(1024, 256) = 1024)
	// Active is 1100, which exceeds 1024.
	g.ApplyConnectionLimits(config.ConnectionLimits{MaxConnections: 256, MaxConnectionsPerSource: 1})

	// All 1100 active connections must be kept!
	snap := g.SourceClients()
	if len(snap.Clients) != count {
		t.Fatalf("expected all %d active clients retained, got %d", count, len(snap.Clients))
	}

	// New connection from 10.3.0.1 cannot be admitted
	raw := l.enqueue("10.3.0.1")
	_ = l.Close()
	_, _ = gl.Accept()

	if !raw.isClosed {
		t.Fatal("expected rejection")
	}
	rej := g.Rejections()
	// Active is 1100 >= total 256 -> TotalLimit rejection, NOT SourceLimit!
	// (Ensure recordnil is not mislabeled as source limit)
	if rej.SourceLimit != 0 {
		t.Fatalf("SourceLimit must be 0, got %d", rej.SourceLimit)
	}
	if rej.TotalLimit != 1 {
		t.Fatalf("expected TotalLimit=1, got %d", rej.TotalLimit)
	}

	for _, c := range conns {
		_ = c.Close()
	}
}

func TestApplyConnectionLimitsConcurrentCloseAndLimitRaces(t *testing.T) {
	g := NewConnectionGuard(50, 10, nil)
	l := newFakeListener("127.0.0.1:17890")
	gl := g.Wrap(l)
	defer func() { _ = l.Close() }()

	var wg sync.WaitGroup
	const iterations = 50

	// Goroutine 1: Rapid limit updates
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range iterations {
			if i%2 == 0 {
				g.ApplyConnectionLimits(config.ConnectionLimits{MaxConnections: 20, MaxConnectionsPerSource: 5})
			} else {
				g.ApplyConnectionLimits(config.ConnectionLimits{MaxConnections: 80, MaxConnectionsPerSource: 20})
			}
			time.Sleep(time.Millisecond)
		}
	}()

	// Goroutine 2: Reads capacity and source clients
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range iterations {
			_ = g.Capacity()
			_ = g.SourceClients()
			_ = g.Rejections()
			time.Sleep(time.Millisecond)
		}
	}()

	// Goroutine 3 & 4: Open and close connections
	for worker := range 2 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			ip := fmt.Sprintf("10.4.0.%d", w+1)
			for range iterations {
				l.enqueue(ip)
				c, err := gl.Accept()
				if err == nil && c != nil {
					time.Sleep(100 * time.Microsecond)
					_ = c.Close()
				}
			}
		}(worker)
	}

	wg.Wait()
	if cap := g.Capacity(); cap.ActiveConnections != 0 {
		t.Fatalf("expected 0 active connections after test, got %d", cap.ActiveConnections)
	}
}
