package proxy

import (
	"encoding/base64"
	"io"
	"net"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPAndSOCKSShareAuthenticationLimit(t *testing.T) {
	s := testServer(t, true)
	for range 20 {
		r := httptest.NewRequest("GET", "http://example.invalid/", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:wrong")))
		w := httptest.NewRecorder()
		s.HTTPHandler().ServeHTTP(w, r)
		if w.Code != 407 {
			t.Fatal(w.Code)
		}
	}
	r := httptest.NewRequest("GET", "http://example.invalid/", nil)
	r.RemoteAddr = "127.0.0.1:4321"
	r.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:password")))
	w := httptest.NewRecorder()
	s.HTTPHandler().ServeHTTP(w, r)
	if w.Code != 429 {
		t.Fatal("valid credential bypassed temporary block", w.Code)
	}
	server, client := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	// net.Pipe has no IP; wrap it to represent the same socket peer as HTTP.
	done := make(chan bool, 1)
	go func() { done <- s.socksAuthenticate(peerConn{Conn: server}) }()
	if _, err := client.Write([]byte{1, 4, 'u', 's', 'e', 'r', 8, 'p', 'a', 's', 's', 'w', 'o', 'r', 'd'}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 1 || <-done {
		t.Fatal("SOCKS bypassed HTTP failure block")
	}
}

// Clients normally send a first request without credentials to receive the
// 407 challenge. Those challenges are not password guesses and must not lock
// out the same source's correct credentials.
func TestProxyChallengesDoNotCountAsAuthenticationFailures(t *testing.T) {
	s := testServer(t, true)
	for range 25 {
		r := httptest.NewRequest("GET", "http://example.invalid/", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		s.HTTPHandler().ServeHTTP(w, r)
		if w.Code != 407 {
			t.Fatal(w.Code)
		}
	}
	s.dialer = blockingDialer{}
	s.dialTimeout = 10 * time.Millisecond
	r := httptest.NewRequest("GET", "http://example.invalid/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:password")))
	w := httptest.NewRecorder()
	s.HTTPHandler().ServeHTTP(w, r)
	if w.Code != 502 {
		t.Fatalf("valid credentials after challenges: status=%d want=502 from outbound", w.Code)
	}
}

type peerConn struct{ net.Conn }

func (peerConn) RemoteAddr() net.Addr { return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5678} }
