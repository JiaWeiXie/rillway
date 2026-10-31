package proxy

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"rillway/internal/config"
	"rillway/internal/engine"
	"rillway/internal/hostlocal"
	"rillway/internal/outbound"
	"strings"
	"testing"
	"time"
)

func hostLocalProtocolServer(t *testing.T, mode string) (*Server, *hostlocal.State) {
	t.Helper()
	c := config.Default(t.TempDir())
	c.Outbounds, c.Rules, c.Adaptive.Candidates = []config.Outbound{{ID: "direct", Type: "direct", Enabled: true, PublicInternet: true}}, nil, []string{"direct"}
	p, err := outbound.New(t.Context(), c.Outbounds[0])
	if err != nil {
		t.Fatal(err)
	}
	e := engine.New(c, map[string]outbound.Provider{"direct": p})
	t.Cleanup(func() { _ = e.Close(); _ = p.Close() })
	policy, err := hostlocal.Compile(mode, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := hostlocal.NewState(policy)
	e.SetHostLocalPolicy(state)
	s, err := New(e, c.Security, c.ConnectionLimits, testSourcePolicy(t), "")
	if err != nil {
		t.Fatal(err)
	}
	s.SetHostLocalPolicy(state)
	e.SetDestinationPolicy(s.DestinationPolicy)
	t.Cleanup(func() { _ = s.Close() })
	return s, state
}

func TestOwnListenerPolicyNormalizesIPv6ZoneBeforeExceptions(t *testing.T) {
	s := testServer(t, false)
	s.updateEndpoints(func(endpoints *localEndpoints) {
		endpoints.internal = []netip.AddrPort{netip.MustParseAddrPort("[::1]:17892")}
		endpoints.proxy = []netip.AddrPort{netip.MustParseAddrPort("[::1]:17890")}
	})
	for _, address := range []string{"[::1%lo]:17892", "[::1%other]:17890"} {
		if s.DestinationPolicy(address) == "" {
			t.Fatal("zone annotation bypassed own-listener protection", address)
		}
	}
}

// These real protocol peers are loopback. External/own-interface/unknown source
// decisions are tested separately; this does not claim a remote-host deployment.
func TestHostLocalModesAcrossWireProtocols(t *testing.T) {
	for _, mode := range []string{hostlocal.Open, hostlocal.LocalClientsOnly, hostlocal.Strict} {
		for _, protocol := range []string{"http", "connect", "socks_tcp", "socks_udp"} {
			t.Run(mode+"/"+protocol, func(t *testing.T) {
				s, state := hostLocalProtocolServer(t, mode)
				denied := mode == hostlocal.Strict
				switch protocol {
				case "http":
					origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "fixture") }))
					defer origin.Close()
					front := httptest.NewUnstartedServer(s.HTTPHandler())
					front.Listener = s.GuardListener(front.Listener)
					front.Start()
					defer front.Close()
					proxyURL, _ := url.Parse(front.URL)
					transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
					defer transport.CloseIdleConnections()
					client := &http.Client{Transport: transport, Timeout: time.Second}
					response, err := client.Get(strings.Replace(origin.URL, "127.0.0.1", "localhost", 1))
					if err != nil {
						t.Fatal(err)
					}
					body, err := io.ReadAll(response.Body)
					_ = response.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
					want := http.StatusOK
					if denied {
						want = http.StatusBadGateway
					}
					if response.StatusCode != want {
						t.Fatal("HTTP decision", response.StatusCode, want)
					}
					if denied && strings.Contains(string(body), "127.0.0.1") {
						t.Fatal("client error disclosed internal destination")
					}
				case "connect":
					origin := echoListener(t)
					front := httptest.NewServer(s.HTTPHandler())
					defer front.Close()
					client, err := net.Dial("tcp", front.Listener.Addr().String())
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = client.Close() }()
					_ = client.SetDeadline(time.Now().Add(time.Second))
					address := strings.Replace(origin.Addr().String(), "127.0.0.1", "localhost", 1)
					if _, err := fmt.Fprintf(client, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", address, address); err != nil {
						t.Fatal(err)
					}
					response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
					if err != nil {
						t.Fatal(err)
					}
					want := http.StatusOK
					if denied {
						want = http.StatusBadGateway
					}
					if response.StatusCode != want {
						t.Fatal("CONNECT decision", response.StatusCode, want)
					}
				case "socks_tcp":
					origin := echoListener(t)
					client := startSOCKS(t, s)
					if _, err := client.Write([]byte{5, 1, 0}); err != nil {
						t.Fatal(err)
					}
					var choice [2]byte
					if _, err := io.ReadFull(client, choice[:]); err != nil || choice != [2]byte{5, 0} {
						t.Fatal(choice, err)
					}
					port := netip.MustParseAddrPort(origin.Addr().String()).Port()
					request := append([]byte{5, 1, 0, 3, 9}, []byte("localhost")...)
					request = binary.BigEndian.AppendUint16(request, port)
					if _, err := client.Write(request); err != nil {
						t.Fatal(err)
					}
					var reply [10]byte
					if _, err := io.ReadFull(client, reply[:]); err != nil {
						t.Fatal(err)
					}
					want := byte(0)
					if denied {
						want = 2
					}
					if reply[1] != want {
						t.Fatal("SOCKS decision", reply[1], want)
					}
				case "socks_udp":
					control, bound := udpSOCKSAssociation(t, s, false, 0)
					origin := udpEcho(t).LocalAddr().(*net.UDPAddr)
					client := udpClient(t, control)
					payload := []byte("datagram")
					request := udpRequestIPv4(origin.IP, origin.Port, payload)
					if _, err := client.WriteToUDP(request, bound); err != nil {
						t.Fatal(err)
					}
					if denied {
						readNoUDP(t, client)
					} else {
						readUDPEcho(t, client, bound, request[:10], payload)
					}
				}
				got := state.Snapshot()
				var want uint64
				if denied {
					want = 1
				}
				if got.ActualDenials != want || got.HypotheticalLocalClientsOnlyDenials != 0 {
					t.Fatal("loopback operation counters", got)
				}
			})
		}
	}
}
