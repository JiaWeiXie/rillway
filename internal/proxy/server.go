// Package proxy implements HTTP forwarding, CONNECT and SOCKS5 TCP without TLS
// interception. Access controls are checked before parsing destination traffic.
package proxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"rillway/internal/authguard"
	"rillway/internal/config"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Dialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type Server struct {
	dialer             Dialer
	allowed            []netip.Prefix
	username, password string
	transport          *http.Transport
	mu                 sync.Mutex
	connections        map[io.ReadWriteCloser]struct{}
	listeners          map[net.Listener]struct{}
	closed             bool
	authFailures       *authguard.Limiter
	guard              *ConnectionGuard
	handshakeTimeout   time.Duration
	dialTimeout        time.Duration
	endpoints          []netip.AddrPort
	internalEndpoints  []netip.AddrPort
	localIPs           map[netip.Addr]bool
}

func New(dialer Dialer, security config.Security, password string) (*Server, error) {
	if dialer == nil {
		return nil, errors.New("proxy dialer is required")
	}
	if (security.ProxyUsername == "") != (password == "") {
		return nil, errors.New("proxy username and password must both be configured")
	}
	if len(security.ProxyUsername) > 255 || len(password) > 255 {
		return nil, errors.New("SOCKS credentials must fit in 255 bytes")
	}
	s := &Server{dialer: dialer, username: security.ProxyUsername, password: password, connections: make(map[io.ReadWriteCloser]struct{}), listeners: make(map[net.Listener]struct{}), authFailures: authguard.New(), handshakeTimeout: 10 * time.Second, dialTimeout: 15 * time.Second}
	s.guard = NewConnectionGuard(256, 64, s.allowedClient)
	s.localIPs = map[netip.Addr]bool{netip.MustParseAddr("127.0.0.1"): true, netip.MustParseAddr("::1"): true}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("local interface addresses: %w", err)
	}
	for _, address := range addresses {
		if prefix, err := netip.ParsePrefix(address.String()); err == nil {
			s.localIPs[prefix.Addr().Unmap()] = true
		}
	}
	allowed := security.AllowedClients
	if len(allowed) == 0 {
		allowed = []string{"127.0.0.0/8", "::1/128"}
	}
	for _, cidr := range allowed {
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nil, fmt.Errorf("allowed client %q: %w", cidr, err)
		}
		s.allowed = append(s.allowed, p)
	}
	s.transport = &http.Transport{Proxy: nil, DialContext: s.dialContext, DisableKeepAlives: true, ResponseHeaderTimeout: 30 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second}
	return s, nil
}

func (s *Server) allowedClient(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	for _, p := range s.allowed {
		if p.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

func (s *Server) authenticated(r *http.Request) bool {
	if s.username == "" {
		return true
	}
	kind, value, ok := strings.Cut(r.Header.Get("Proxy-Authorization"), " ")
	if !ok || !strings.EqualFold(kind, "Basic") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return false
	}
	u, p, ok := strings.Cut(string(decoded), ":")
	return ok && authguard.SecretEqual(u, s.username) && authguard.SecretEqual(p, s.password)
}

func (s *Server) HTTPHandler() http.Handler { return http.HandlerFunc(s.serveHTTP) }

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		http.Error(w, "proxy is shutting down", http.StatusServiceUnavailable)
		return
	}
	if !s.allowedClient(r.RemoteAddr) {
		http.Error(w, "proxy source is not allowed", http.StatusForbidden)
		return
	}
	if s.username != "" && !s.authFailures.Allow(r.RemoteAddr) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many proxy authentication attempts", http.StatusTooManyRequests)
		return
	}
	if !s.authenticated(r) {
		// A request without credentials is the normal 407 challenge, not a guess.
		if r.Header.Get("Proxy-Authorization") != "" {
			s.authFailures.Failure(r.RemoteAddr)
		}
		w.Header().Set("Proxy-Authenticate", `Basic realm="Rillway"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		s.connect(w, r)
		return
	}
	if r.URL == nil || (r.URL.Scheme != "http" && r.URL.Scheme != "https") || r.URL.Host == "" || r.URL.User != nil {
		http.Error(w, "absolute HTTP or HTTPS URL required", http.StatusBadRequest)
		return
	}
	request := r.Clone(r.Context())
	request.RequestURI = ""
	websocket := strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && headerToken(r.Header, "Connection", "upgrade")
	removeHopHeaders(request.Header)
	if websocket {
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "websocket")
	}
	request.Header.Del("Proxy-Authorization")
	request.Header.Del("Proxy-Authenticate")
	response, err := s.transport.RoundTrip(request)
	if err != nil {
		http.Error(w, "outbound connection failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusSwitchingProtocols {
		if !websocket || !strings.EqualFold(response.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "unexpected protocol upgrade", http.StatusBadGateway)
			return
		}
		s.upgrade(w, r, response)
		return
	}
	removeHopHeaders(response.Header)
	for k, values := range response.Header {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(response.StatusCode)
	writer := &responseWriter{writer: w, controller: http.NewResponseController(w)}
	if _, err = io.Copy(writer, response.Body); err != nil {
		// Headers may already be visible to the client. Abort the HTTP response so
		// net/http does not turn a truncated upstream body into a clean EOF.
		panic(http.ErrAbortHandler)
	}
}

type responseWriter struct {
	writer     io.Writer
	controller *http.ResponseController
}

func (w *responseWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if err != nil {
		return n, err
	}
	if flushErr := w.controller.Flush(); flushErr != nil {
		return n, flushErr
	}
	return n, nil
}

func headerToken(h http.Header, key, token string) bool {
	for _, value := range h.Values(key) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func (s *Server) upgrade(w http.ResponseWriter, r *http.Request, response *http.Response) {
	upstream, ok := response.Body.(io.ReadWriteCloser)
	if !ok {
		http.Error(w, "upstream does not support bidirectional upgrade", http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "HTTP/1 upgrade required", http.StatusNotImplemented)
		return
	}
	client, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	if !s.track(client) {
		return
	}
	defer s.untrack(client)
	if !s.track(upstream) {
		return
	}
	defer s.untrack(upstream)
	removeHopHeaders(response.Header)
	response.Header.Set("Connection", "Upgrade")
	response.Header.Set("Upgrade", "websocket")
	if _, err = buffer.WriteString("HTTP/1.1 101 Switching Protocols\r\n"); err != nil {
		return
	}
	if err = response.Header.Write(buffer); err != nil {
		return
	}
	if _, err = buffer.WriteString("\r\n"); err != nil {
		return
	}
	if err = buffer.Flush(); err != nil {
		return
	}
	// After hijacking, net/http can cancel the request context on a client
	// write-half-close. Keep receiving the upstream reply; Server.Close owns
	// shutdown of both tracked sockets.
	relay(context.WithoutCancel(r.Context()), &bufferedConn{Conn: client, reader: buffer.Reader}, upstream)
}

func removeHopHeaders(h http.Header) {
	for _, value := range h.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
	for _, key := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(key)
	}
}

func validAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || strings.ContainsAny(host, " \t\r\n/\\?#@") {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	if !validAddress(r.Host) {
		http.Error(w, "CONNECT requires host:port", http.StatusBadRequest)
		return
	}
	upstream, err := s.dialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, "outbound connection failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = upstream.Close() }()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "HTTP/1 CONNECT required", http.StatusNotImplemented)
		return
	}
	client, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	if !s.track(client) {
		return
	}
	defer s.untrack(client)
	if !s.track(upstream) {
		return
	}
	defer s.untrack(upstream)
	if _, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err = buffer.Flush(); err != nil {
		return
	}
	relay(context.WithoutCancel(r.Context()), &bufferedConn{Conn: client, reader: buffer.Reader}, upstream)
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *bufferedConn) CloseWrite() error {
	if v, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return v.CloseWrite()
	}
	return c.Close()
}

func (s *Server) track(c io.ReadWriteCloser) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		_ = c.Close()
		return false
	}
	s.connections[c] = struct{}{}
	return true
}

func (s *Server) untrack(c io.ReadWriteCloser) { s.mu.Lock(); delete(s.connections, c); s.mu.Unlock() }

func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	for c := range s.connections {
		_ = c.Close()
	}
	for l := range s.listeners {
		_ = l.Close()
	}
	s.mu.Unlock()
	s.transport.CloseIdleConnections()
	return nil
}

// relay waits for both stream directions. A clean EOF is forwarded as a TCP
// half-close; any read or write error closes both sides so dead peers do not
// hold admission slots or retired providers.
func relay(ctx context.Context, a, b io.ReadWriteCloser) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = a.Close()
			_ = b.Close()
		case <-done:
		}
	}()
	var wg sync.WaitGroup
	copyStream := func(dst, src io.ReadWriteCloser) {
		defer wg.Done()
		_, err := io.Copy(dst, src)
		if err != nil {
			_ = a.Close()
			_ = b.Close()
			return
		}
		if c, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}
	wg.Add(2)
	go copyStream(a, b)
	copyStream(b, a)
	wg.Wait()
	close(done)
}
