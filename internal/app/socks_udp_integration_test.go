package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/proxy"
	"strconv"
	"testing"
	"time"
)

func TestRealStackSOCKS5UDPAssociateIntegration(t *testing.T) {
	echoServer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	echoDone := make(chan struct{})
	go func() {
		defer close(echoDone)
		var buf [2048]byte
		for {
			n, peer, err := echoServer.ReadFromUDP(buf[:])
			if err != nil {
				return
			}
			if _, err := echoServer.WriteToUDP(buf[:n], peer); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = echoServer.Close(); <-echoDone })

	// Observe the fixed-rule destination itself, not just the client's replies.
	vpnTarget, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vpnTarget.Close() })
	vpnPort := vpnTarget.LocalAddr().(*net.UDPAddr).Port

	cfg := config.Default(t.TempDir())
	// A disabled private VPN reaches the unavailable-provider error path;
	// a loopback WARP destination would instead fail the private-route policy.
	cfg.Outbounds = append(cfg.Outbounds, config.Outbound{
		ID: "vpn-disabled", Type: "wireguard", Enabled: false,
		ConfigFile: filepath.Join(t.TempDir(), "unused.conf"),
	})
	cfg.Rules = []config.Rule{{ID: "fixed-vpn-fail", Ports: []string{strconv.Itoa(vpnPort)}, Outbound: "vpn-disabled"}}
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	r, err := New(t.Context(), cfgPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	policy, err := config.CompileSourceAccess(cfg.SourceAccess)
	if err != nil {
		t.Fatal(err)
	}
	p, err := proxy.New(r.Engine, cfg.Security, cfg.ConnectionLimits, policy, "")
	if err != nil {
		t.Fatal(err)
	}
	r.attachProxy(p)
	r.Engine.SetDestinationPolicy(p.DestinationPolicy)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		_ = p.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	serveDone := make(chan error, 1)
	go func() { serveDone <- p.ServeSOCKS(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		_ = p.Close()
		if err := <-serveDone; err != nil {
			t.Errorf("SOCKS shutdown: %v", err)
		}
	})

	control, err := net.DialTimeout("tcp4", listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	if err := control.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	var greeting [2]byte
	if _, err := io.ReadFull(control, greeting[:]); err != nil || greeting != [2]byte{5, 0} {
		t.Fatalf("SOCKS greeting = %v, err = %v", greeting, err)
	}
	if _, err := control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	var reply [10]byte
	if _, err := io.ReadFull(control, reply[:]); err != nil || !bytes.Equal(reply[:4], []byte{5, 0, 0, 1}) {
		t.Fatalf("UDP ASSOCIATE reply = %v, err = %v", reply, err)
	}
	relay := &net.UDPAddr{IP: net.IP(reply[4:8]), Port: int(binary.BigEndian.Uint16(reply[8:]))}
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: control.LocalAddr().(*net.TCPAddr).IP})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	packet := []byte{0, 0, 0, 1, 127, 0, 0, 1, 0, 0, 'h', 'e', 'l', 'l', 'o'}
	binary.BigEndian.PutUint16(packet[8:10], uint16(echoServer.LocalAddr().(*net.UDPAddr).Port))
	var response [2048]byte
	exchangeEcho := func() {
		t.Helper()
		if _, err := client.WriteToUDP(packet, relay); err != nil {
			t.Fatal(err)
		}
		if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		n, from, err := client.ReadFromUDP(response[:])
		if err != nil || !bytes.Equal(response[:n], packet) || from.String() != relay.String() {
			t.Fatalf("UDP echo = %q from %v, err = %v", response[:n], from, err)
		}
	}
	exchangeEcho()
	flows := r.Engine.Snapshot().Flows
	if len(flows) != 1 || flows[0].Outbound != "direct" || flows[0].UploadBytes != 5 || flows[0].DownloadBytes != 5 {
		t.Fatalf("UDP flow accounting = %+v", flows)
	}

	blockedPacket := []byte{0, 0, 0, 1, 127, 0, 0, 1, 0, 0, 'd', 'r', 'o', 'p'}
	binary.BigEndian.PutUint16(blockedPacket[8:10], uint16(vpnPort))
	if _, err := client.WriteToUDP(blockedPacket, relay); err != nil {
		t.Fatal(err)
	}
	// The relay processes client packets serially. A subsequent echo also
	// proves the failed target was handled while the association remained usable.
	exchangeEcho()
	if err := vpnTarget.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	n, peer, err := vpnTarget.ReadFromUDP(response[:])
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("fixed VPN route: receiver got %q from %v, err = %v; want timeout with no datagram", response[:n], peer, err)
	}

	// Positive control: change only the route, then use the same association,
	// target listener and payload. A leaked direct packet would hit this receiver.
	next := r.Config()
	next.Rules[0].Outbound = "direct"
	if err := r.Apply(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	if err := vpnTarget.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.WriteToUDP(blockedPacket, relay); err != nil {
		t.Fatal(err)
	}
	n, peer, err = vpnTarget.ReadFromUDP(response[:])
	if err != nil || !bytes.Equal(response[:n], blockedPacket[10:]) {
		t.Fatalf("direct positive control: receiver got %q, err = %v", response[:n], err)
	}
	if _, err := vpnTarget.WriteToUDP(response[:n], peer); err != nil {
		t.Fatal(err)
	}
	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, from, err := client.ReadFromUDP(response[:])
	if err != nil || !bytes.Equal(response[:n], blockedPacket) || from.String() != relay.String() {
		t.Fatalf("direct positive control: SOCKS reply = %q from %v, err = %v", response[:n], from, err)
	}
}
