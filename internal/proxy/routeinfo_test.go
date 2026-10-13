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
	"rillway/internal/config"
	"rillway/internal/routeinfo"
	"strings"
	"syscall"
	"testing"
	"time"
)

type routeMetadataCall struct {
	info             routeinfo.Info
	network, address string
}

type routeMetadataDialer struct {
	calls chan routeMetadataCall
}

func (d routeMetadataDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.calls <- routeMetadataCall{routeinfo.From(ctx), network, address}
	return nil, syscall.ECONNREFUSED
}

func metadataServer(t *testing.T) (*Server, <-chan routeMetadataCall) {
	t.Helper()
	calls := make(chan routeMetadataCall, 1)
	s, err := New(routeMetadataDialer{calls}, config.Security{}, config.ConnectionLimits{}, testSourcePolicy(t), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, calls
}

func assertRouteMetadata(t *testing.T, calls <-chan routeMetadataCall, source netip.Addr, inbound, address string) {
	t.Helper()
	select {
	case got := <-calls:
		want := routeMetadataCall{routeinfo.Info{SourceIP: source, Inbound: inbound}, "tcp", address}
		if got != want {
			t.Fatalf("outbound dial metadata = %+v, want %+v", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request never reached outbound dialer")
	}
}

func TestHTTPIngressSuppliesDialRouteMetadata(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodConnect} {
		t.Run(method, func(t *testing.T) {
			s, calls := metadataServer(t)
			front := httptest.NewServer(s.HTTPHandler())
			defer front.Close()
			client, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			if err = client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			source := client.LocalAddr().(*net.TCPAddr).AddrPort().Addr().Unmap()
			target := "http://metadata.example:443/path"
			if method == http.MethodConnect {
				target = "metadata.example:443"
			}
			if _, err = fmt.Fprintf(client, "%s %s HTTP/1.1\r\nHost: metadata.example:443\r\nConnection: close\r\n\r\n", method, target); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: method})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusBadGateway {
				t.Fatalf("status = %d, want refused outbound response", response.StatusCode)
			}
			assertRouteMetadata(t, calls, source, "http", "metadata.example:443")
		})
	}
}

func TestHTTPRouteMetadataUnmapsAndAllowsUnknownSource(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodConnect} {
		for _, tt := range []struct {
			remote string
			source netip.Addr
		}{
			{"[::ffff:203.0.113.7]:1234", netip.MustParseAddr("203.0.113.7")},
			{"[2001:db8::7]:1234", netip.MustParseAddr("2001:db8::7")},
			{"unknown", netip.Addr{}},
		} {
			t.Run(method+"/"+tt.remote, func(t *testing.T) {
				s, calls := metadataServer(t)
				request := httptest.NewRequest(method, "http://metadata.example:443/path", nil)
				request.Host = "metadata.example:443"
				request.RemoteAddr = tt.remote
				recorder := httptest.NewRecorder()
				s.HTTPHandler().ServeHTTP(recorder, request)
				if recorder.Code != http.StatusBadGateway {
					t.Fatalf("status = %d, want 502", recorder.Code)
				}
				assertRouteMetadata(t, calls, tt.source, "http", "metadata.example:443")
			})
		}
	}
}

func TestSOCKSIngressSuppliesDialRouteMetadata(t *testing.T) {
	s, calls := metadataServer(t)
	client := startSOCKS(t, s)
	source := client.LocalAddr().(*net.TCPAddr).AddrPort().Addr().Unmap()
	if _, err := client.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	var choice [2]byte
	if _, err := io.ReadFull(client, choice[:]); err != nil || choice != [2]byte{5, 0} {
		t.Fatalf("SOCKS greeting = %v, err = %v", choice, err)
	}
	host := "metadata.example"
	request := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	request = append(request, 1, 187) // destination port 443
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	var reply [10]byte
	if _, err := io.ReadFull(client, reply[:]); err != nil || reply[0] != 5 || reply[1] != 5 {
		t.Fatalf("SOCKS refused reply = %v, err = %v", reply, err)
	}
	assertRouteMetadata(t, calls, source, "socks5", "metadata.example:443")
}
