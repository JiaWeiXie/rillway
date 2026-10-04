package outbound

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"strings"
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
		if _, e := ParseWireGuard(s); e == nil {
			t.Fatal("unsafe configuration accepted")
		} else if strings.Contains(e.Error(), "private-secret") {
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

// Real WireGuard packets travel over loopback UDP between two userspace
// devices. This proves encrypted transport and netstack compatibility without
// any credentials, privileged TUN interfaces or external VPN service.
func TestWireGuardEncryptedLoopback(t *testing.T) {
	privateA := make([]byte, 32)
	privateB := make([]byte, 32)
	if _, e := rand.Read(privateA); e != nil {
		t.Fatal(e)
	}
	if _, e := rand.Read(privateB); e != nil {
		t.Fatal(e)
	}
	publicA, e := curve25519.X25519(privateA, curve25519.Basepoint)
	if e != nil {
		t.Fatal(e)
	}
	publicB, e := curve25519.X25519(privateB, curve25519.Basepoint)
	if e != nil {
		t.Fatal(e)
	}
	tun, network, e := netstack.CreateNetTUN([]netip.Addr{netip.MustParseAddr("10.77.0.1")}, nil, 1420)
	if e != nil {
		t.Fatal(e)
	}
	server := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	defer server.Close()
	ipc := fmt.Sprintf("private_key=%s\nlisten_port=0\npublic_key=%s\nallowed_ip=10.77.0.2/32\n", hex.EncodeToString(privateA), hex.EncodeToString(publicB))
	if e = server.IpcSet(ipc); e != nil {
		t.Fatal(e)
	}
	if e = server.Up(); e != nil {
		t.Fatal(e)
	}
	status, e := server.IpcGet()
	if e != nil {
		t.Fatal(e)
	}
	port := ""
	for _, line := range strings.Split(status, "\n") {
		if v, ok := strings.CutPrefix(line, "listen_port="); ok {
			port = v
		}
	}
	if port == "" || port == "0" {
		t.Fatal("server UDP bind failed")
	}
	listener, e := network.ListenTCPAddrPort(netip.MustParseAddrPort("10.77.0.1:8080"))
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = listener.Close() }()
	done := make(chan error, 1)
	go func() {
		c, e := listener.Accept()
		if e != nil {
			done <- e
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		buf := make([]byte, 5)
		if _, e = io.ReadFull(c, buf); e == nil {
			_, e = c.Write(buf)
		}
		done <- e
	}()
	profile := fmt.Sprintf("[Interface]\nPrivateKey = %s\nAddress = 10.77.0.2/24\n[Peer]\nPublicKey = %s\nEndpoint = 127.0.0.1:%s\nAllowedIPs = 10.77.0.1/32\n", base64.StdEncoding.EncodeToString(privateB), base64.StdEncoding.EncodeToString(publicA), port)
	filename := filepath.Join(t.TempDir(), "tunnel.conf")
	if e = os.WriteFile(filename, []byte(profile), 0o600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	provider, e := newWireGuard(ctx, config.Outbound{ID: "wg", ConfigFile: filename})
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = provider.Close() }()
	if _, e = provider.DialContext(ctx, "tcp", "host.invalid:8080"); e == nil {
		t.Fatal("missing tunnel DNS fell back")
	}
	if _, e = provider.DialContext(ctx, "tcp", "1.1.1.1:443"); e == nil {
		t.Fatal("AllowedIPs ignored")
	}
	c, e := provider.DialContext(ctx, "tcp4", "10.77.0.1:8080")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, e = c.Write([]byte("hello")); e != nil {
		t.Fatal(e)
	}
	buf := make([]byte, 5)
	if _, e = io.ReadFull(c, buf); e != nil || string(buf) != "hello" {
		t.Fatalf("echo %q: %v", buf, e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}
