package outbound

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"rillway/internal/hostlocal"
	"sync"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// All DNS traffic is confined to this UDP fixture, including retries/searches.
func candidateResolver(t *testing.T) *net.Resolver {
	t.Helper()
	server, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 4096)
		for {
			n, peer, err := server.ReadFrom(buffer)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if err := query.Unpack(buffer[:n]); err != nil || len(query.Questions) != 1 {
				continue
			}
			q := query.Questions[0]
			response := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true, RecursionDesired: query.RecursionDesired, RecursionAvailable: true}, Questions: query.Questions}
			switch q.Type {
			case dnsmessage.TypeA:
				response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}, Body: &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}}})
			case dnsmessage.TypeAAAA:
				response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeAAAA, Class: dnsmessage.ClassINET}, Body: &dnsmessage.AAAAResource{AAAA: netip.IPv6Loopback().As16()}})
			}
			body, err := response.Pack()
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := server.WriteTo(body, peer); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = server.Close(); <-done })
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp4", server.LocalAddr().String())
	}}
}

func TestDirectRealDNSCandidatesRespectFinalIngressOutcome(t *testing.T) {
	for _, tc := range []struct {
		name              string
		exception, accept bool
		denied            uint64
	}{
		{"all policy denied", false, false, 1},
		{"blocked then allowed", true, true, 0},
		{"blocked then transport failure", true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// ::1 is refused before connect; the accepted target binds the default
			// IPv4 loopback, without requiring an extra alias on macOS.
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			dst := netip.MustParseAddrPort(listener.Addr().String())
			if !tc.accept {
				_ = listener.Close()
			}
			var rules []hostlocal.RuleSpec
			if tc.exception {
				rules = []hostlocal.RuleSpec{{IP: dst.Addr(), Lo: dst.Port(), Hi: dst.Port(), AnySource: true}}
			}
			p, err := hostlocal.Compile(hostlocal.Strict, rules, nil)
			if err != nil {
				t.Fatal(err)
			}
			state := hostlocal.NewState(p)
			source := netip.MustParseAddr("192.0.2.2")
			event := state.Begin(source, dst.Port())
			var mu sync.Mutex
			var attempts []netip.AddrPort
			ctx := WithResolvedGuard(t.Context(), func(_ string, target netip.AddrPort) error {
				mu.Lock()
				attempts = append(attempts, target)
				mu.Unlock()
				d := p.CheckIP(target, source)
				event.ObserveCandidate(target, d)
				if d.Denied {
					return hostlocal.ErrDenied
				}
				return nil
			})
			_, port, _ := net.SplitHostPort(listener.Addr().String())
			conn, err := (&direct{id: "direct-alias", resolver: candidateResolver(t)}).DialContext(ctx, "tcp", net.JoinHostPort("candidates.test.", port))
			if tc.accept {
				if err != nil {
					t.Fatal(err)
				}
				ip := netip.MustParseAddr(conn.(interface{ DestinationIP() string }).DestinationIP())
				if ip != dst.Addr() {
					t.Fatal("wrong final numeric identity", ip)
				}
				event.Finalize(dst, p.CheckIP(dst, source))
				_ = conn.Close()
				peer, err := listener.Accept()
				if err != nil {
					t.Fatal(err)
				}
				_ = peer.Close()
			} else if err == nil {
				_ = conn.Close()
				t.Fatal("unexpected connection")
			}
			if !tc.exception {
				var operation *net.OpError
				if !errors.Is(err, hostlocal.ErrDenied) || !errors.As(err, &operation) {
					t.Fatal("lost wrapped policy identity", err)
				}
			}
			event.Complete(err)
			got := state.Snapshot()
			if got.ActualDenials != tc.denied || got.HypotheticalLocalClientsOnlyDenials != tc.denied {
				t.Fatal("candidate errors inflated ingress counters", got)
			}
			mu.Lock()
			observed := append([]netip.AddrPort(nil), attempts...)
			mu.Unlock()
			if len(observed) < 2 || observed[0].Addr() != netip.IPv6Loopback() || observed[1].Addr() != dst.Addr() {
				t.Fatal("fixture did not exercise ordered multiple numeric attempts", observed)
			}
		})
	}
}
