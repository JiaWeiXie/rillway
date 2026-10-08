package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"rillway/internal/access"
	"rillway/internal/config"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func testSourcePolicy(t *testing.T) *access.Policy {
	t.Helper()
	p, err := config.CompileSourceAccess(config.Default(t.TempDir()).SourceAccess)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func testServer(t *testing.T, auth bool) *Server {
	t.Helper()
	sc := config.Security{}
	password := ""
	if auth {
		sc.ProxyUsername = "user"
		password = "password"
	}
	s, err := New(&net.Dialer{Timeout: time.Second}, sc, testSourcePolicy(t), password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestHTTPProxyStripsCredentialsAndHopHeaders(t *testing.T) {
	seen := make(chan http.Header, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.Header().Set("Keep-Alive", "timeout=60")
		_, _ = w.Write([]byte("response"))
	}))
	defer origin.Close()
	s := testServer(t, true)
	front := httptest.NewServer(s.HTTPHandler())
	defer front.Close()
	proxyURL, _ := url.Parse(front.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 5 * time.Second}
	request, err := http.NewRequest(http.MethodGet, origin.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:password")))
	request.Header.Set("Connection", "X-Hop")
	request.Header.Set("X-Hop", "private")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	if string(body) != "response" || response.Header.Get("Keep-Alive") != "" {
		t.Fatalf("unexpected response: %s %+v", body, response.Header)
	}
	headers := <-seen
	if headers.Get("Proxy-Authorization") != "" || headers.Get("X-Hop") != "" {
		t.Fatalf("leaked proxy credentials/headers: %+v", headers)
	}
}

func TestHTTPProxyFlushesStreamingResponses(t *testing.T) {
	originReady := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		close(originReady)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer origin.Close()

	s := testServer(t, false)
	front := httptest.NewServer(s.HTTPHandler())
	defer front.Close()
	proxyURL, _ := url.Parse(front.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL, nil)
	result := make(chan error, 1)
	go func() {
		response, err := (&http.Client{Transport: transport}).Do(request)
		if err == nil {
			defer func() { _ = response.Body.Close() }()
			_, err = bufio.NewReader(response.Body).ReadString('\n')
		}
		result <- err
	}()
	<-originReady
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		cancel()
		<-result
		t.Fatal("flushed event was withheld by proxy")
	}
}

func TestHTTPProxyAbortsTruncatedUpstreamResponse(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = buffer.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n7\r\npartial\r\n")
		_ = buffer.Flush()
	}))
	defer origin.Close()

	s := testServer(t, false)
	front := httptest.NewServer(s.HTTPHandler())
	defer front.Close()
	proxyURL, _ := url.Parse(front.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport, Timeout: time.Second}).Get(origin.URL)
	if err != nil {
		return
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err == nil {
		t.Fatalf("truncated upstream accepted as complete response: %q", data)
	}
}

type blockingDialer struct{}

func (blockingDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// Tunnel providers honor only the context, so plain HTTP forwarding needs the
// same bounded dial as CONNECT and SOCKS5 instead of hanging the client.
func TestHTTPProxyBoundsOutboundDial(t *testing.T) {
	s, err := New(blockingDialer{}, config.Security{}, testSourcePolicy(t), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.dialTimeout = 50 * time.Millisecond
	request := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); s.HTTPHandler().ServeHTTP(recorder, request) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("plain HTTP outbound dial was not bounded")
	}
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status=%d want=%d", recorder.Code, http.StatusBadGateway)
	}
}

func TestRemoveConnectionNominatedHeaders(t *testing.T) {
	h := http.Header{"Connection": {"keep-alive, X-Internal"}, "X-Internal": {"private"}, "Keep-Alive": {"timeout=60"}, "Content-Type": {"text/plain"}}
	removeHopHeaders(h)
	if h.Get("X-Internal") != "" || h.Get("Keep-Alive") != "" || h.Get("Content-Type") != "text/plain" {
		t.Fatalf("incorrect hop-header filtering: %+v", h)
	}
}

func TestHTTPAuthentication(t *testing.T) {
	s := testServer(t, true)
	for _, tt := range []struct {
		remote, auth string
		code         int
	}{{"127.0.0.1:1234", "", 407}, {"127.0.0.1:1234", "Basic " + base64.StdEncoding.EncodeToString([]byte("user:wrong")), 407}} {
		request := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		request.RemoteAddr = tt.remote
		request.Header.Set("Proxy-Authorization", tt.auth)
		recorder := httptest.NewRecorder()
		s.HTTPHandler().ServeHTTP(recorder, request)
		if recorder.Code != tt.code {
			t.Fatalf("status=%d want=%d", recorder.Code, tt.code)
		}
	}
}

func echoListener(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = c.Close() }(); _, _ = io.Copy(c, c) }()
		}
	}()
	return l
}

func TestCONNECTPreservesBufferedPayloadAndHalfClose(t *testing.T) {
	origin := echoListener(t)
	s := testServer(t, false)
	front := httptest.NewServer(s.HTTPHandler())
	defer front.Close()
	c, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\nearly", origin.Addr(), origin.Addr())
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(c)
	response, err := http.ReadResponse(r, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		t.Fatalf("CONNECT failed: %s", response.Status)
	}
	buffer := make([]byte, 5)
	if _, err = io.ReadFull(r, buffer); err != nil || string(buffer) != "early" {
		t.Fatalf("lost buffered bytes %q %v", buffer, err)
	}
	if err = c.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err = r.ReadByte(); err != io.EOF {
		t.Fatalf("half-close did not terminate: %v", err)
	}
}

func startSOCKS(t *testing.T, s *Server) net.Conn {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.ServeSOCKS(ctx, l) }()
	t.Cleanup(func() {
		cancel()
		_ = s.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("SOCKS did not shut down")
		}
	})
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	return c
}

func TestSOCKSAuthenticatedTCP(t *testing.T) {
	s := testServer(t, true)
	c := startSOCKS(t, s)
	origin := echoListener(t)
	_, _ = c.Write([]byte{5, 2, 0, 2})
	var choice [2]byte
	if _, err := io.ReadFull(c, choice[:]); err != nil || choice != [2]byte{5, 2} {
		t.Fatalf("auth choice %v %v", choice, err)
	}
	_, _ = c.Write(append([]byte{1, 4, 'u', 's', 'e', 'r', 8}, []byte("password")...))
	if _, err := io.ReadFull(c, choice[:]); err != nil || choice != [2]byte{1, 0} {
		t.Fatalf("auth %v %v", choice, err)
	}
	addr := origin.Addr().(*net.TCPAddr)
	request := []byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 0}
	binary.BigEndian.PutUint16(request[8:], uint16(addr.Port))
	_, _ = c.Write(request)
	var reply [10]byte
	if _, err := io.ReadFull(c, reply[:]); err != nil || reply[1] != 0 {
		t.Fatalf("SOCKS reply %v %v", reply, err)
	}
	_, _ = c.Write([]byte("hello"))
	buffer := make([]byte, 5)
	if _, err := io.ReadFull(c, buffer); err != nil || string(buffer) != "hello" {
		t.Fatalf("TCP relay %q %v", buffer, err)
	}
}

func TestSOCKSRejectsUDPAndAuthDowngrade(t *testing.T) {
	t.Run("UDP", func(t *testing.T) {
		s := testServer(t, false)
		c := startSOCKS(t, s)
		_, _ = c.Write([]byte{5, 1, 0})
		var choice [2]byte
		_, _ = io.ReadFull(c, choice[:])
		_, _ = c.Write([]byte{5, 3, 0, 1})
		var reply [10]byte
		if _, err := io.ReadFull(c, reply[:]); err != nil || reply[1] != 7 {
			t.Fatalf("UDP accepted: %v %v", reply, err)
		}
	})
	t.Run("auth", func(t *testing.T) {
		s := testServer(t, true)
		c := startSOCKS(t, s)
		_, _ = c.Write([]byte{5, 1, 0})
		var reply [2]byte
		if _, err := io.ReadFull(c, reply[:]); err != nil || reply[1] != 255 {
			t.Fatalf("auth downgraded: %v %v", reply, err)
		}
	})
}

type flakyListener struct {
	net.Listener
	failed atomic.Bool
}

func emfile() error {
	return &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept", syscall.EMFILE)}
}

func (l *flakyListener) Accept() (net.Conn, error) {
	if l.failed.CompareAndSwap(false, true) {
		return nil, emfile()
	}
	return l.Listener.Accept()
}

// Descriptor exhaustion is transient. Stopping the SOCKS5 listener would end
// the service and drop every proxied stream until the supervisor restarts it.
func TestSOCKSAcceptSurvivesTemporaryError(t *testing.T) {
	s := testServer(t, false)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.ServeSOCKS(ctx, &flakyListener{Listener: l}) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("SOCKS did not shut down")
		}
	})
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = c.Write([]byte{5, 1, 0})
	var choice [2]byte
	if _, err := io.ReadFull(c, choice[:]); err != nil || choice != [2]byte{5, 0} {
		select {
		case serveErr := <-done:
			done <- serveErr
			t.Fatalf("SOCKS listener stopped after temporary accept error: %v", serveErr)
		default:
		}
		t.Fatalf("method choice %v %v", choice, err)
	}
}

// exhaustedListener fails every Accept and reports when the loop has entered a
// long backoff (5 ms doubling: the eighth failure waits 640 ms).
type exhaustedListener struct {
	net.Listener
	accepts    atomic.Int32
	backingOff chan struct{}
}

func (l *exhaustedListener) Accept() (net.Conn, error) {
	if l.accepts.Add(1) == 8 {
		close(l.backingOff)
	}
	return nil, emfile()
}

func TestSOCKSShutdownInterruptsAcceptBackoff(t *testing.T) {
	s := testServer(t, false)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	exhausted := &exhaustedListener{Listener: l, backingOff: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.ServeSOCKS(ctx, exhausted) }()
	select {
	case <-exhausted.backingOff:
	case err := <-done:
		t.Fatalf("SOCKS stopped during temporary errors: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("accept loop did not retry")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("shutdown waited for the accept backoff")
	}
}

func TestSOCKSContextCancelsIncompleteHandshake(t *testing.T) {
	s := testServer(t, false)
	c := startSOCKS(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := c.Read(b[:]); err == nil {
		t.Fatal("connection remained open")
	}
}

type slowDialer struct{ delay time.Duration }

func (d slowDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	select {
	case <-time.After(d.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

// A slow but successful outbound dial must not inherit the client handshake
// deadline; otherwise the success reply is never written and the client sees EOF.
func TestSOCKSSlowDialOutlivesHandshakeDeadline(t *testing.T) {
	s, err := New(slowDialer{delay: 300 * time.Millisecond}, config.Security{}, testSourcePolicy(t), "")
	if err != nil {
		t.Fatal(err)
	}
	s.handshakeTimeout = 100 * time.Millisecond
	c := startSOCKS(t, s)
	origin := echoListener(t)
	_, _ = c.Write([]byte{5, 1, 0})
	var choice [2]byte
	if _, err := io.ReadFull(c, choice[:]); err != nil || choice != [2]byte{5, 0} {
		t.Fatalf("method choice %v %v", choice, err)
	}
	request := []byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 0}
	binary.BigEndian.PutUint16(request[8:], uint16(origin.Addr().(*net.TCPAddr).Port))
	_, _ = c.Write(request)
	var reply [10]byte
	if _, err := io.ReadFull(c, reply[:]); err != nil || reply[1] != 0 {
		t.Fatalf("SOCKS reply %v %v", reply, err)
	}
	_, _ = c.Write([]byte("slow"))
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(c, buffer); err != nil || string(buffer) != "slow" {
		t.Fatalf("TCP relay %q %v", buffer, err)
	}
}

// deadlineLog records client deadline changes and outbound dials in call order.
type deadlineLog struct {
	mu     sync.Mutex
	events []string
	dialed time.Time
	reply  time.Time // first nonzero deadline set after the dial returned
}

type deadlineSpyConn struct {
	*net.TCPConn
	log *deadlineLog
}

func (c deadlineSpyConn) SetDeadline(t time.Time) error {
	c.log.mu.Lock()
	if t.IsZero() {
		c.log.events = append(c.log.events, "clear")
	} else {
		c.log.events = append(c.log.events, "set")
		if !c.log.dialed.IsZero() && c.log.reply.IsZero() {
			c.log.reply = t
		}
	}
	c.log.mu.Unlock()
	return c.TCPConn.SetDeadline(t)
}

type loggingDialer struct{ log *deadlineLog }

func (d loggingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	d.log.mu.Lock()
	d.log.events = append(d.log.events, "dial")
	d.log.dialed = time.Now()
	d.log.mu.Unlock()
	return conn, err
}

// A client that stops reading after the dial must not hold the handshake
// goroutine forever: the reply gets a fresh deadline, cleared only for relay.
func TestSOCKSReplyWriteHasFreshDeadlineAfterDial(t *testing.T) {
	log := &deadlineLog{}
	s, err := New(loggingDialer{log: log}, config.Security{}, testSourcePolicy(t), "")
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := l.Accept()
		if err != nil {
			return
		}
		s.socksConnection(ctx, deadlineSpyConn{TCPConn: conn.(*net.TCPConn), log: log})
	}()
	defer func() { cancel(); <-done }()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	origin := echoListener(t)
	request := []byte{5, 1, 0, 5, 1, 0, 1, 127, 0, 0, 1, 0, 0}
	binary.BigEndian.PutUint16(request[11:], uint16(origin.Addr().(*net.TCPAddr).Port))
	_, _ = c.Write(request)
	var reply [12]byte
	if _, err := io.ReadFull(c, reply[:]); err != nil || reply[3] != 0 {
		t.Fatalf("SOCKS reply %v %v", reply, err)
	}
	_, _ = c.Write([]byte("ping"))
	if _, err := io.ReadFull(c, reply[:4]); err != nil || string(reply[:4]) != "ping" {
		t.Fatalf("TCP relay %q %v", reply[:4], err)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if got, want := strings.Join(log.events, ","), "set,clear,dial,set,clear"; got != want {
		t.Fatalf("deadline sequence = %s, want %s", got, want)
	}
	if earliest := log.dialed.Add(s.handshakeTimeout); log.reply.Before(earliest) {
		t.Fatalf("reply deadline %v predates dial completion plus handshake timeout %v", log.reply, earliest)
	}
}

func TestHTTPWebsocketUpgradeIsBidirectional(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "missing upgrade", 400)
			return
		}
		c, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = buf.Flush()
		_, _ = io.Copy(c, buf)
	}))
	defer origin.Close()
	s := testServer(t, false)
	front := httptest.NewServer(s.HTTPHandler())
	defer front.Close()
	c, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = fmt.Fprintf(c, "GET %s/ws HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n", origin.URL, strings.TrimPrefix(origin.URL, "http://"))
	reader := bufio.NewReader(c)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil || response.StatusCode != 101 {
		t.Fatalf("upgrade response: %+v %v", response, err)
	}
	_, _ = c.Write([]byte("payload"))
	buffer := make([]byte, 7)
	if _, err = io.ReadFull(reader, buffer); err != nil || string(buffer) != "payload" {
		t.Fatalf("upgrade relay: %q %v", buffer, err)
	}
}

func FuzzSOCKSAddress(f *testing.F) {
	f.Add(byte(1), []byte{127, 0, 0, 1, 0, 80})
	f.Add(byte(3), []byte{3, 'a', '.', 'b', 1, 187})
	f.Add(byte(4), make([]byte, 18))
	f.Fuzz(func(t *testing.T, kind byte, data []byte) {
		address, err := readAddress(bytes.NewReader(data), kind)
		if err == nil && !validAddress(address) {
			t.Fatalf("invalid parsed address %q", address)
		}
	})
}
