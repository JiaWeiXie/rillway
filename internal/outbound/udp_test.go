package outbound

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"rillway/internal/config"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/net/dns/dnsmessage"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
	"tailscale.com/types/views"
)

func TestDirectUDPDatagrams(t *testing.T) {
	for _, tc := range []struct {
		network, listen, host, destinationIP string
	}{
		{"udp", "udp4", "127.0.0.1", "127.0.0.1"},
		{"udp4", "udp4", "localhost", "127.0.0.1"},
		{"udp6", "udp6", "::1", "::1"},
	} {
		t.Run(tc.network, func(t *testing.T) {
			listenAddr := "127.0.0.1:0"
			if tc.listen == "udp6" {
				listenAddr = "[::1]:0"
			}
			server, err := net.ListenPacket(tc.listen, listenAddr)
			if err != nil {
				if tc.listen == "udp6" {
					t.Skipf("IPv6 loopback unavailable: %v", err)
				}
				t.Fatal(err)
			}
			defer func() { _ = server.Close() }()
			_, port, err := net.SplitHostPort(server.LocalAddr().String())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			client, err := (&direct{id: "direct"}).DialContext(ctx, tc.network, net.JoinHostPort(tc.host, port))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			if got := client.(interface{ DestinationIP() string }).DestinationIP(); got != tc.destinationIP {
				t.Fatalf("destination IP = %q, want %q", got, tc.destinationIP)
			}
			deadline := time.Now().Add(5 * time.Second)
			if err := server.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			if err := client.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			packets := []string{"a", "second datagram"}
			for _, packet := range packets {
				if n, err := client.Write([]byte(packet)); err != nil || n != len(packet) {
					t.Fatalf("write %q: %d, %v", packet, n, err)
				}
			}
			var buf [64]byte
			for _, packet := range packets {
				n, sender, err := server.ReadFrom(buf[:])
				if err != nil || string(buf[:n]) != packet {
					t.Fatalf("upstream datagram = %q, %v; want %q", buf[:n], err, packet)
				}
				if n, err := server.WriteTo(buf[:n], sender); err != nil || n != len(packet) {
					t.Fatalf("echo %q: %d, %v", packet, n, err)
				}
			}
			for _, packet := range packets {
				n, err := client.Read(buf[:])
				if err != nil || string(buf[:n]) != packet {
					t.Fatalf("reply datagram = %q, %v; want %q", buf[:n], err, packet)
				}
			}
		})
	}
}

func TestWARPUDPUnsupportedWithoutProxyDial(t *testing.T) {
	provider := newWARP(config.Outbound{ID: "warp", ProxyAddress: "127.0.0.1:1"})
	for _, network := range []string{"udp", "udp4", "udp6"} {
		conn, err := provider.DialContext(t.Context(), network, "localhost:53")
		if conn != nil || !errors.Is(err, ErrUDPUnsupported) {
			t.Fatalf("%s: connection = %v, error = %v", network, conn, err)
		}
	}
}

func wireGuardUDPEchoPeer(t *testing.T) (func(string) string, <-chan error) {
	t.Helper()
	privateA, privateB := make([]byte, 32), make([]byte, 32)
	if _, err := rand.Read(privateA); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(privateB); err != nil {
		t.Fatal(err)
	}
	publicA, err := curve25519.X25519(privateA, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	publicB, err := curve25519.X25519(privateB, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	tun, network, err := netstack.CreateNetTUN([]netip.Addr{netip.MustParseAddr("10.77.0.1")}, nil, 1420)
	if err != nil {
		t.Fatal(err)
	}
	server := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	t.Cleanup(server.Close)
	ipc := fmt.Sprintf("private_key=%s\nlisten_port=0\npublic_key=%s\nallowed_ip=10.77.0.2/32\n", hex.EncodeToString(privateA), hex.EncodeToString(publicB))
	if err := server.IpcSet(ipc); err != nil {
		t.Fatal(err)
	}
	if err := server.Up(); err != nil {
		t.Fatal(err)
	}
	status, err := server.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	port := ""
	for _, line := range strings.Split(status, "\n") {
		if value, ok := strings.CutPrefix(line, "listen_port="); ok {
			port = value
		}
	}
	if port == "" || port == "0" {
		t.Fatal("WireGuard peer UDP bind failed")
	}
	listener, err := network.ListenUDPAddrPort(netip.MustParseAddrPort("10.77.0.1:8081"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	echoed := make(chan error, 1)
	go func() {
		var buf [64]byte
		for _, packet := range []string{"one", "second datagram"} {
			n, peer, err := listener.ReadFrom(buf[:])
			if err != nil {
				echoed <- err
				return
			}
			if string(buf[:n]) != packet {
				echoed <- fmt.Errorf("upstream UDP datagram = %q, want %q", buf[:n], packet)
				return
			}
			if _, err := listener.WriteTo(buf[:n], peer); err != nil {
				echoed <- err
				return
			}
		}
		echoed <- nil
	}()
	profile := func(endpointHost string) string {
		return fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.77.0.2/24\n[Peer]\nPublicKey = %s\nEndpoint = %s:%s\nAllowedIPs = 10.77.0.1/32\n", base64.StdEncoding.EncodeToString(privateB), base64.StdEncoding.EncodeToString(publicA), endpointHost, port)
	}
	return profile, echoed
}

func TestWireGuardEncryptedUDPAndRouteGuards(t *testing.T) {
	profile, echoed := wireGuardUDPEchoPeer(t)
	provider, err := newWireGuardWith(t.Context(), config.Outbound{ID: "wg", ConfigFile: writeWireGuardProfile(t, profile("127.0.0.1"))}, lookupEndpoint, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	for _, tc := range []struct{ network, address, reason string }{
		{"udp", "private.invalid:8081", "no tunnel DNS"},
		{"udp4", "127.0.0.1:8081", "AllowedIPs"},
		{"udp6", "[::1]:8081", "AllowedIPs"},
		{"udp6", "10.77.0.1:8081", "requested family"},
	} {
		if conn, err := provider.DialContext(t.Context(), tc.network, tc.address); conn != nil || err == nil || !strings.Contains(err.Error(), tc.reason) {
			t.Fatalf("%s %s: connection = %v, error = %v; want %s", tc.network, tc.address, conn, err, tc.reason)
		}
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if conn, err := provider.DialContext(canceled, "udp4", "10.77.0.1:8081"); conn != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled WireGuard UDP dial returned %v, %v", conn, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	client, err := provider.DialContext(ctx, "udp4", "10.77.0.1:8081")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if got := client.(interface{ DestinationIP() string }).DestinationIP(); got != "10.77.0.1" {
		t.Fatalf("UDP destination IP = %q", got)
	}
	if err := client.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var buf [64]byte
	for _, packet := range []string{"one", "second datagram"} {
		if n, err := client.Write([]byte(packet)); err != nil || n != len(packet) {
			t.Fatalf("write UDP datagram: %d, %v", n, err)
		}
		n, err := client.Read(buf[:])
		if err != nil || string(buf[:n]) != packet {
			t.Fatalf("WireGuard reply = %q, %v; want %q", buf[:n], err, packet)
		}
	}
	if err := <-echoed; err != nil {
		t.Fatal(err)
	}
}

func TestTailscaleUDPNamesStayInTailnet(t *testing.T) {
	peerIP := netip.MustParseAddr("100.70.0.2")
	status := &ipnstate.Status{
		CurrentTailnet: &ipnstate.TailnetStatus{MagicDNSSuffix: "example.ts"},
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{{}: {
			TailscaleIPs: []netip.Addr{peerIP},
		}},
	}
	dialCount, queryCount := 0, 0
	answerIP := [4]byte{100, 70, 0, 2}
	provider := &tailscale{
		status: func(context.Context) (*ipnstate.Status, error) { return status, nil },
		addresses: func() (netip.Addr, netip.Addr) {
			return netip.MustParseAddr("100.70.0.1"), netip.Addr{}
		},
		queryMagicDNS: func(_ context.Context, name, queryType string) ([]byte, error) {
			queryCount++
			if name != "peer.example.ts." || queryType != "A" {
				return nil, fmt.Errorf("unexpected MagicDNS query %s %s", name, queryType)
			}
			dnsName := dnsmessage.MustNewName(name)
			msg := dnsmessage.Message{
				Header: dnsmessage.Header{Response: true},
				Answers: []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: dnsName, Class: dnsmessage.ClassINET, Type: dnsmessage.TypeA},
					Body:   &dnsmessage.AResource{A: answerIP},
				}},
			}
			return msg.Pack()
		},
		dialNetstackUDP: func(_ context.Context, source netip.Addr, destination netip.AddrPort) (net.Conn, error) {
			dialCount++
			if source != netip.MustParseAddr("100.70.0.1") || destination != netip.MustParseAddrPort("100.70.0.2:53") {
				return nil, fmt.Errorf("unexpected UDP tunnel %s to %s", source, destination)
			}
			left, right := net.Pipe()
			t.Cleanup(func() { _ = right.Close() })
			return left, nil
		},
	}
	if conn, err := provider.DialContext(t.Context(), "udp4", "private.internal:53"); conn != nil || err == nil || !strings.Contains(err.Error(), "public fallback is disabled") {
		t.Fatalf("private name escaped tunnel DNS: %v, %v", conn, err)
	}
	if queryCount != 0 || dialCount != 0 {
		t.Fatalf("private name used MagicDNS or transport: %d queries, %d dials", queryCount, dialCount)
	}
	conn, err := provider.DialContext(t.Context(), "udp4", "peer:53")
	if err != nil {
		t.Fatal(err)
	}
	if got := conn.(interface{ DestinationIP() string }).DestinationIP(); got != peerIP.String() {
		t.Fatalf("MagicDNS destination IP = %q", got)
	}
	_ = conn.Close()
	answerIP = [4]byte{1, 1, 1, 1}
	if conn, err := provider.DialContext(t.Context(), "udp4", "peer:53"); conn != nil || err == nil {
		t.Fatalf("unowned MagicDNS address reached transport: %v, %v", conn, err)
	}
	if conn, err := provider.DialContext(t.Context(), "udp4", "1.1.1.1:53"); conn != nil || err == nil {
		t.Fatalf("unowned literal address reached transport: %v, %v", conn, err)
	}
	if queryCount != 2 || dialCount != 1 {
		t.Fatalf("MagicDNS queries = %d, UDP dials = %d; want 2, 1", queryCount, dialCount)
	}
}

func TestTailscaleUDPUsesOnlyRoutedNetstack(t *testing.T) {
	routes := views.SliceOf([]netip.Prefix{netip.MustParsePrefix("192.168.50.0/24"), netip.MustParsePrefix("0.0.0.0/0")})
	status := &ipnstate.Status{Peer: map[key.NodePublic]*ipnstate.PeerStatus{{}: {
		TailscaleIPs:  []netip.Addr{netip.MustParseAddr("100.70.0.2"), netip.MustParseAddr("fd7a:115c:a1e0::2")},
		PrimaryRoutes: &routes,
	}}}
	calls := 0
	provider := &tailscale{
		status: func(context.Context) (*ipnstate.Status, error) { return status, nil },
		addresses: func() (netip.Addr, netip.Addr) {
			return netip.MustParseAddr("100.70.0.1"), netip.MustParseAddr("fd7a:115c:a1e0::1")
		},
		dialNetstackUDP: func(_ context.Context, source netip.Addr, destination netip.AddrPort) (net.Conn, error) {
			calls++
			if source.Is4() != destination.Addr().Is4() {
				return nil, fmt.Errorf("wrong Tailscale source %s for %s", source, destination)
			}
			left, right := net.Pipe()
			t.Cleanup(func() { _ = right.Close() })
			return left, nil
		},
		dialNetstack: func(context.Context, netip.Addr, netip.AddrPort) (net.Conn, error) {
			t.Fatal("UDP request used the TCP tunnel")
			return nil, errors.New("unexpected TCP dial")
		},
	}
	for _, destination := range []string{"100.70.0.2:53", "192.168.50.42:53", "[fd7a:115c:a1e0::2]:53"} {
		conn, err := provider.dialUDPTunnel(t.Context(), netip.MustParseAddrPort(destination))
		if err != nil {
			t.Fatalf("routed UDP %s: %v", destination, err)
		}
		_ = conn.Close()
	}
	if calls != 3 {
		t.Fatalf("netstack UDP dial count = %d, want 3", calls)
	}
	for _, destination := range []string{"100.70.0.9:53", "1.1.1.1:53", "127.0.0.1:53"} {
		conn, err := provider.dialUDPTunnel(t.Context(), netip.MustParseAddrPort(destination))
		if conn != nil || err == nil {
			t.Fatalf("unowned UDP %s reached netstack: %v, %v", destination, conn, err)
		}
	}
	status = &ipnstate.Status{}
	if conn, err := provider.dialUDPTunnel(t.Context(), netip.MustParseAddrPort("100.70.0.2:53")); conn != nil || err == nil {
		t.Fatalf("withdrawn route reached netstack: %v, %v", conn, err)
	}
	if calls != 3 {
		t.Fatalf("netstack dialed unowned destination: %d calls", calls)
	}
}

func TestTailscaleUDPClosesDialCanceledBeforeReturn(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var left net.Conn
	provider := &tailscale{
		status: func(context.Context) (*ipnstate.Status, error) {
			return &ipnstate.Status{TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.70.0.2")}}, nil
		},
		addresses: func() (netip.Addr, netip.Addr) { return netip.MustParseAddr("100.70.0.1"), netip.Addr{} },
		dialNetstackUDP: func(context.Context, netip.Addr, netip.AddrPort) (net.Conn, error) {
			var right net.Conn
			left, right = net.Pipe()
			t.Cleanup(func() { _ = right.Close() })
			cancel()
			return left, nil
		},
	}
	conn, err := provider.dialUDPTunnel(ctx, netip.MustParseAddrPort("100.70.0.2:53"))
	if conn != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled dial returned %v, %v", conn, err)
	}
	if _, err := left.Write([]byte("closed")); err == nil {
		t.Fatal("canceled UDP socket was not closed")
	}
}
