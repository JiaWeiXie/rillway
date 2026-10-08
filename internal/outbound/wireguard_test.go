package outbound

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

const wgSample = "[Interface]\nPrivateKey = AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=\nAddress = 10.0.0.2/24, fd00::2/64\nDNS = 10.0.0.1\nMTU = 1420\n[Peer]\nPublicKey = AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI=\nEndpoint = vpn.example.com:51820\nAllowedIPs = 0.0.0.0/0, ::/0\nPersistentKeepalive = 25\n"

func TestParseWireGuard(t *testing.T) {
	c, err := ParseWireGuard(wgSample)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Addresses) != 2 || len(c.DNS) != 1 || c.MTU != 1420 || !c.allows(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("fields missing")
	}
	for _, s := range []string{strings.Replace(wgSample, "MTU = 1420", "PostUp = arbitrary command", 1), strings.Replace(wgSample, "DNS = 10.0.0.1", "DNS = internal.example", 1), strings.Replace(wgSample, "MTU = 1420", "MTU = 12", 1), strings.Replace(wgSample, "0.0.0.0/0, ::/0", "10.0.1.0/24", 1), strings.Replace(wgSample, "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", "private-secret", 1)} {
		if _, err := ParseWireGuard(s); err == nil {
			t.Fatal("unsafe configuration accepted")
		} else if strings.Contains(err.Error(), "private-secret") {
			t.Fatal("secret in parse error")
		}
	}
}

func FuzzParseWireGuard(f *testing.F) {
	f.Add(wgSample)
	f.Add("[Interface]\nPostUp=x")
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 1<<20 {
			t.Skip()
		}
		_, _ = ParseWireGuard(s)
	})
}

func wireGuardEchoPeer(t *testing.T) (func(string) string, <-chan error) {
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
	if err = server.IpcSet(ipc); err != nil {
		t.Fatal(err)
	}
	if err = server.Up(); err != nil {
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
		t.Fatal("server UDP bind failed")
	}
	listener, err := network.ListenTCPAddrPort(netip.MustParseAddrPort("10.77.0.1:8080"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	echoed := make(chan error, 1)
	go func() {
		client, err := listener.Accept()
		if err != nil {
			echoed <- err
			return
		}
		defer func() { _ = client.Close() }()
		_ = client.SetDeadline(time.Now().Add(10 * time.Second))
		buf := make([]byte, 5)
		if _, err = io.ReadFull(client, buf); err == nil {
			_, err = client.Write(buf)
		}
		echoed <- err
	}()
	profile := func(endpointHost string) string {
		return fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.77.0.2/24\n[Peer]\nPublicKey = %s\nEndpoint = %s:%s\nAllowedIPs = 10.77.0.1/32\n", base64.StdEncoding.EncodeToString(privateB), base64.StdEncoding.EncodeToString(publicA), endpointHost, port)
	}
	return profile, echoed
}

func writeWireGuardProfile(t *testing.T, profile string) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "tunnel.conf")
	if err := os.WriteFile(filename, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	return filename
}

func echoThroughWireGuard(t *testing.T, provider *wireGuard, echoed <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := provider.DialContext(ctx, "tcp4", "10.77.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = client.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err = io.ReadFull(client, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("echo %q: %v", buf, err)
	}
	if err = <-echoed; err != nil {
		t.Fatal(err)
	}
}

func TestWireGuardEncryptedLoopback(t *testing.T) {
	profile, echoed := wireGuardEchoPeer(t)
	provider, err := newWireGuardWith(t.Context(), config.Outbound{ID: "wg", ConfigFile: writeWireGuardProfile(t, profile("127.0.0.1"))}, lookupEndpoint, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	if _, err = provider.DialContext(t.Context(), "tcp", "host.invalid:8080"); err == nil {
		t.Fatal("missing tunnel DNS fell back")
	}
	if _, err = provider.DialContext(t.Context(), "tcp", "1.1.1.1:443"); err == nil {
		t.Fatal("AllowedIPs ignored")
	}
	echoThroughWireGuard(t, provider, echoed)
}

func TestWireGuardRotatesStaleEndpoint(t *testing.T) {
	profile, echoed := wireGuardEchoPeer(t)
	provider, err := newWireGuardWith(t.Context(), config.Outbound{ID: "wg", ConfigFile: writeWireGuardProfile(t, profile("wg-peer.test"))}, func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("127.0.0.1")}, nil
	}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	provider.refreshEndpoints(t.Context(), time.Now())
	status, _ := provider.device.IpcGet()
	if !strings.Contains(status, "endpoint=192.0.2.1:") {
		t.Fatalf("endpoint rotated without traffic: %s", status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = provider.DialContext(ctx, "tcp4", "10.77.0.1:8080"); err == nil {
		t.Fatal("dead first endpoint connected")
	}
	provider.refreshEndpoints(t.Context(), time.Now())
	status, _ = provider.device.IpcGet()
	if !strings.Contains(status, "endpoint=127.0.0.1:") {
		t.Fatalf("endpoint did not rotate: %s", status)
	}
	echoThroughWireGuard(t, provider, echoed)
}

func TestWireGuardRetriesBootstrapDNS(t *testing.T) {
	profile, echoed := wireGuardEchoPeer(t)
	var calls atomic.Int32
	provider, err := newWireGuardWith(t.Context(), config.Outbound{ID: "wg", ConfigFile: writeWireGuardProfile(t, profile("wg-peer.test"))}, func(context.Context, string) ([]netip.Addr, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("temporary DNS failure")
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	if status := provider.Status(t.Context()); status.State != "unavailable" || status.Detail != "WireGuard endpoint bootstrap DNS failed" {
		t.Fatalf("unexpected status: %+v", status)
	}
	started := time.Now()
	if _, err = provider.DialContext(t.Context(), "tcp4", "10.77.0.1:8080"); err == nil || err.Error() != "WireGuard endpoint bootstrap DNS failed" {
		t.Fatalf("dial error = %v", err)
	}
	if time.Since(started) >= time.Second {
		t.Fatal("unresolved endpoint did not fail fast")
	}
	provider.refreshEndpoints(t.Context(), time.Now())
	if status := provider.Status(t.Context()); status.State != "ready" {
		t.Fatalf("unexpected status after refresh: %+v", status)
	}
	echoThroughWireGuard(t, provider, echoed)
}

func TestWireGuardRefreshOutlivesConstructorContext(t *testing.T) {
	filename := writeWireGuardProfile(t, wgSample)
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	provider, err := newWireGuardWith(ctx, config.Outbound{ID: "wg", ConfigFile: filename}, func(context.Context, string) ([]netip.Addr, error) {
		calls.Add(1)
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	cancel()
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer dialCancel()
	_, _ = provider.DialContext(dialCtx, "tcp", "host.invalid:80")
	deadline := time.Now().Add(time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("resolver calls = %d, want refresh after constructor context cancellation", got)
	}
}

func TestWireGuardUnresolvedHostnameDoesNotBlockLiteralPeer(t *testing.T) {
	literalKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	profile := strings.Replace(wgSample, "DNS = 10.0.0.1\n", "", 1)
	profile = strings.Replace(profile, "AllowedIPs = 0.0.0.0/0, ::/0", "AllowedIPs = 10.99.0.0/16", 1) +
		fmt.Sprintf("[Peer]\nPublicKey = %s\nEndpoint = 127.0.0.1:51821\nAllowedIPs = 10.77.0.0/16\n", literalKey)
	provider, err := newWireGuardWith(t.Context(), config.Outbound{ID: "wg", ConfigFile: writeWireGuardProfile(t, profile)}, func(context.Context, string) ([]netip.Addr, error) {
		return nil, errors.New("temporary DNS failure")
	}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = provider.DialContext(ctx, "tcp4", "10.77.0.1:8080")
	if err != nil && err.Error() == "WireGuard endpoint bootstrap DNS failed" {
		t.Fatal("unresolved hostname blocked a configured literal-IP peer")
	}
}
