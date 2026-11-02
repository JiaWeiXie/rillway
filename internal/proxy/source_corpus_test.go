package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"rillway/internal/access"
	"rillway/internal/config"
	"rillway/internal/hostlocal"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type corpusAddr string

func (a corpusAddr) Network() string { return "tcp" }
func (a corpusAddr) String() string  { return string(a) }

type corpusConn struct {
	net.Conn
	listener *corpusListener
	once     sync.Once
}

func (c *corpusConn) Read(b []byte) (int, error) {
	c.listener.reads.Add(1)
	return c.Conn.Read(b)
}

func (c *corpusConn) RemoteAddr() net.Addr {
	if c.listener.peer != "" {
		return corpusAddr(c.listener.peer)
	}
	return c.Conn.RemoteAddr()
}

func (c *corpusConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.listener.closed <- struct{}{} })
	return err
}

type corpusListener struct {
	net.Listener
	peer   string
	reads  atomic.Int64
	closed chan struct{}
}

func (l *corpusListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &corpusConn{Conn: c, listener: l}, nil
}

type corpusDialer struct{ calls atomic.Int64 }

func (d *corpusDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	d.calls.Add(1)
	return nil, errors.New("synthetic outbound refusal")
}

func TestSourceACLAdmissionWire(t *testing.T) {
	for _, protocol := range []string{"http", "connect", "socks"} {
		for _, peer := range []string{"wire", "synthetic-mapped", "synthetic-malformed"} {
			for _, allowed := range []bool{false, true} {
				if peer == "synthetic-malformed" && allowed {
					continue
				}
				t.Run(fmt.Sprintf("%s/%s/allow-%t", protocol, peer, allowed), func(t *testing.T) {
					s := testServer(t, false)
					cidrs := []string{"192.0.2.1/32"} // Spoofed headers claim this allowed source.
					if allowed {
						cidrs = append(cidrs, "127.0.0.1/32")
					}
					policy, err := access.CompileAllowlist(cidrs)
					if err != nil {
						t.Fatal(err)
					}
					s.SetSourcePolicy(policy)
					host, err := hostlocal.Compile(hostlocal.Strict, []hostlocal.RuleSpec{{IP: netip.MustParseAddr("127.0.0.1"), Lo: 18080, Hi: 18080, From: []string{"127.0.0.1/32"}}}, nil)
					if err != nil {
						t.Fatal(err)
					}
					s.SetHostLocalPolicy(hostlocal.NewState(host)) // Matching exception cannot grant source admission.
					dialer := &corpusDialer{}
					s.dialer = dialer
					l, err := net.Listen("tcp4", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					meter := &corpusListener{Listener: l, closed: make(chan struct{}, 4)}
					// Only wire uses the kernel peer. These overrides are synthetic representations.
					if peer == "synthetic-mapped" {
						meter.peer = "[::ffff:127.0.0.1]:1234"
					} else if peer == "synthetic-malformed" {
						meter.peer = "[[127.0.0.1]]:1234"
					}
					server := &http.Server{Handler: s.HTTPHandler(), ReadHeaderTimeout: time.Second}
					done := make(chan struct{})
					go func() {
						defer close(done)
						if protocol == "socks" {
							_ = s.ServeSOCKS(t.Context(), meter)
						} else {
							_ = server.Serve(s.GuardListener(meter))
						}
					}()
					t.Cleanup(func() { _ = l.Close(); _ = server.Close(); _ = s.Close(); <-done })
					var previous string
					for attempt := range 2 {
						client, err := net.DialTimeout("tcp4", l.Addr().String(), time.Second)
						if err != nil {
							t.Fatal(err)
						}
						defer func() { _ = client.Close() }() // Keep ports distinct while the second socket opens.
						if client.LocalAddr().String() == previous {
							t.Fatal("source port did not change")
						}
						previous = client.LocalAddr().String()
						_ = client.SetDeadline(time.Now().Add(2 * time.Second))
						if protocol == "socks" {
							_, _ = client.Write([]byte{5, 1, 0})
						} else {
							request := "GET http://127.0.0.1:18080/ HTTP/1.1"
							if protocol == "connect" {
								request = "CONNECT 127.0.0.1:18080 HTTP/1.1"
							}
							_, _ = fmt.Fprintf(client, "%s\r\nHost: 127.0.0.1:18080\r\nForwarded: for=192.0.2.1\r\nX-Forwarded-For: 192.0.2.1\r\nConnection: close\r\n\r\n", request)
						}
						if !allowed {
							if n, err := client.Read(make([]byte, 1)); n != 0 || err == nil {
								t.Fatal("denied peer received protocol data", n, err)
							} else if e, ok := err.(net.Error); ok && e.Timeout() {
								t.Fatal("denied socket timed out instead of closing")
							}
						} else if protocol == "socks" {
							var choice [2]byte
							if _, err := io.ReadFull(client, choice[:]); err != nil || choice != [2]byte{5, 0} {
								t.Fatal("SOCKS greeting", choice, err)
							}
							_, _ = client.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, 0x46, 0xa0}) // port 18080
							var reply [10]byte
							if _, err := io.ReadFull(client, reply[:]); err != nil || reply[1] == 0 {
								t.Fatal("synthetic outbound failure missing", reply, err)
							}
						} else {
							response, err := http.ReadResponse(bufio.NewReader(client), nil)
							if err != nil {
								t.Fatal(err)
							}
							_ = response.Body.Close()
							if response.StatusCode != http.StatusBadGateway {
								t.Fatal(response.StatusCode)
							}
						}
						select {
						case <-meter.closed:
						case <-time.After(3 * time.Second):
							t.Fatal("connection lifecycle did not finish")
						}
						if allowed {
							if meter.reads.Load() == 0 || dialer.calls.Load() != int64(attempt+1) {
								t.Fatal("positive control did not reach parser and outbound")
							}
						} else if meter.reads.Load() != 0 || dialer.calls.Load() != 0 {
							t.Fatal("denied peer reached parser or outbound")
						}
					}
				})
			}
		}
	}
}

func TestSourceACLAdmissionUpdateRace(t *testing.T) {
	s := testServer(t, false)
	l := newFakeListener("127.0.0.1:17890")
	defer func() { _ = l.Close() }()
	l.enqueue("127.0.0.1")
	c, err := s.GuardListener(l).Accept()
	if err != nil {
		t.Fatal(err)
	}
	deny, err := access.Compile(nil)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, action := range []func(){func() { s.SetSourcePolicy(deny) }, func() {
		s.ApplyConnectionLimits(config.ConnectionLimits{MaxConnections: 1, MaxConnectionsPerSource: 1})
	}, func() { _ = c.Close() }, func() { s.DisconnectSource(netip.MustParseAddr("::ffff:127.0.0.1")) }} {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; action() }()
	}
	close(start)
	wg.Wait()
	if s.Capacity().ActiveConnections != 0 || s.DisconnectSource(netip.MustParseAddr("127.0.0.1")) != 0 {
		t.Fatal("concurrent close/disconnect did not recover capacity")
	}
}
