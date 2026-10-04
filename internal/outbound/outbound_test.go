package outbound

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"rillway/internal/config"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
	"tailscale.com/types/views"
)

func TestWARPModeFailureNeverConnects(t *testing.T) {
	w := newWARP(config.Outbound{ID: "warp"})
	var commands [][]string
	w.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		commands = append(commands, args)
		return []byte("secret license must not escape"), errors.New("exit status 1")
	}
	err := w.Action(context.Background(), "connect", "")
	if err == nil || strings.Contains(err.Error(), "secret license") {
		t.Fatalf("bad error: %v", err)
	}
	var public interface{ PublicMessage() string }
	if !errors.As(err, &public) || !strings.Contains(public.PublicMessage(), "Local Proxy") {
		t.Fatalf("missing safe mode-specific error: %v", err)
	}
	if len(commands) != 1 || !reflect.DeepEqual(commands[0], []string{"--accept-tos", "mode", "proxy"}) {
		t.Fatalf("commands = %v", commands)
	}
}

func TestWARPExplicitActionsAndManualStop(t *testing.T) {
	w := newWARP(config.Outbound{ID: "warp"})
	var commands [][]string
	w.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		commands = append(commands, args)
		return []byte("ok"), nil
	}
	for _, action := range []string{"register", "connect", "disconnect"} {
		if err := w.Action(context.Background(), action, ""); err != nil {
			t.Fatal(err)
		}
	}
	expected := [][]string{{"--accept-tos", "registration", "new"}, {"--accept-tos", "mode", "proxy"}, {"--accept-tos", "proxy", "port", "40000"}, {"--accept-tos", "connect"}, {"--accept-tos", "disconnect"}}
	if !reflect.DeepEqual(commands, expected) {
		t.Fatalf("commands = %v", commands)
	}
	if _, e := w.DialContext(context.Background(), "tcp", "example.com:443"); e == nil {
		t.Fatal("manual stop ignored")
	}
	if s := w.Status(context.Background()); s.State != "stopped" {
		t.Fatal(s)
	}
	if len(commands) != 5 {
		t.Fatal("status restarted WARP")
	}
}

func TestWARPLicenseOutputRedacted(t *testing.T) {
	w := newWARP(config.Outbound{ID: "warp"})
	license := "test-license-sensitive"
	w.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[len(args)-1] != license {
			t.Fatal("license not passed")
		}
		return []byte(license), errors.New("failure containing " + license)
	}
	if err := w.Action(context.Background(), "license", license); err == nil || strings.Contains(err.Error(), license) {
		t.Fatalf("error contains secret: %v", err)
	}
}

func TestWARPStatusSeparatesAccountListenerAndVerification(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	w := newWARP(config.Outbound{ID: "warp", ProxyAddress: ln.Addr().String()})
	calls := 0
	w.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++
		switch args[1] {
		case "--version":
			return []byte("warp-cli 2026.7.1376.0"), nil
		case "settings":
			return []byte("(user set)\tMode: WarpProxy on port 40000\nLicense: sensitive"), nil
		case "status":
			return []byte("Status update: Connected"), nil
		case "registration":
			return []byte("Account type: Unlimited\nLicense: sensitive"), nil
		}
		return nil, errors.New("unexpected command")
	}
	s := w.Status(context.Background())
	if s.Mode != "proxy" || s.Version != "2026.7.1376.0" || s.Account != "Unlimited" || !s.Listener || !s.VerifiedAt.IsZero() {
		t.Fatalf("status = %+v", s)
	}
	_ = w.Status(context.Background())
	if calls != 4 {
		t.Fatalf("status not cached: %d", calls)
	}
	if warpUnlimited("Unlimited: false\nAccount type: Limited") {
		t.Fatal("false Unlimited match")
	}
	if strings.Contains(fmt.Sprint(s), "sensitive") {
		t.Fatal("status leaked registration output")
	}
}

func TestWARPVerificationRejectsUntrustedTLS(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("warp=plus\n")) }))
	defer origin.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		client, e := ln.Accept()
		if e != nil {
			return
		}
		defer func() { _ = client.Close() }()
		_ = client.SetDeadline(time.Now().Add(5 * time.Second))
		var greeting [3]byte
		if _, e = io.ReadFull(client, greeting[:]); e != nil {
			return
		}
		if _, e = client.Write([]byte{5, 0}); e != nil {
			return
		}
		var request [5]byte
		if _, e = io.ReadFull(client, request[:]); e != nil || request[3] != 3 {
			return
		}
		if _, e = io.CopyN(io.Discard, client, int64(request[4])+2); e != nil {
			return
		}
		upstream, e := net.DialTimeout("tcp", strings.TrimPrefix(origin.URL, "https://"), 2*time.Second)
		if e != nil {
			return
		}
		defer func() { _ = upstream.Close() }()
		if _, e = client.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); e != nil {
			return
		}
		back := make(chan struct{})
		go func() { _, _ = io.Copy(client, upstream); close(back) }()
		_, _ = io.Copy(upstream, client)
		_ = upstream.Close()
		<-back
	}()
	w := newWARP(config.Outbound{ID: "warp", ProxyAddress: ln.Addr().String()})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = w.Action(ctx, "verify", ""); err == nil {
		t.Fatal("untrusted endpoint was accepted as verified WARP")
	}
	if !w.verified.IsZero() {
		t.Fatal("untrusted TLS marked verified")
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("mock proxy did not terminate")
	}
}

func TestSOCKSRemoteDNSAndUnknownIP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	done := make(chan error, 1)
	go func() {
		c, e := ln.Accept()
		if e != nil {
			done <- e
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		var greeting [3]byte
		if _, e = io.ReadFull(c, greeting[:]); e != nil {
			done <- e
			return
		}
		if greeting != [3]byte{5, 1, 0} {
			done <- errors.New("bad greeting")
			return
		}
		if _, e = c.Write([]byte{5, 0}); e != nil {
			done <- e
			return
		}
		var req [5]byte
		if _, e = io.ReadFull(c, req[:]); e != nil {
			done <- e
			return
		}
		if req[3] != 3 {
			done <- errors.New("DNS leaked into local resolution")
			return
		}
		host := make([]byte, int(req[4]))
		if _, e = io.ReadFull(c, host); e != nil {
			done <- e
			return
		}
		if string(host) != "only-exists-upstream.invalid" {
			done <- errors.New("wrong domain")
			return
		}
		var port [2]byte
		if _, e = io.ReadFull(c, port[:]); e != nil {
			done <- e
			return
		}
		if _, e = c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); e != nil {
			done <- e
			return
		}
		b, e := io.ReadAll(c)
		if e == nil {
			_, e = c.Write(b)
		}
		done <- e
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialSOCKS(ctx, ln.Addr().String(), "tcp", "only-exists-upstream.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if c.(interface{ DestinationIP() string }).DestinationIP() != "" {
		t.Fatal("invented destination IP")
	}
	if _, err = c.Write([]byte("test")); err != nil {
		t.Fatal(err)
	}
	if err = c.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	if _, err = io.ReadFull(c, b); err != nil || string(b) != "test" {
		t.Fatalf("bad relay %q %v", b, err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSOCKSRejectsUnsupportedFamilyBeforeDial(t *testing.T) {
	for _, tc := range []struct{ network, addr string }{{"tcp4", "example.com:443"}, {"tcp6", "127.0.0.1:443"}, {"udp", "example.com:53"}} {
		if _, err := dialSOCKS(context.Background(), "127.0.0.1:1", tc.network, tc.addr); err == nil || strings.Contains(err.Error(), "listener") {
			t.Fatalf("did not reject before network: %v", err)
		}
	}
}

func TestUnsupportedResolverConfigurationFailsExplicitly(t *testing.T) {
	for _, kind := range []string{"direct", "warp"} {
		if _, err := New(context.Background(), config.Outbound{ID: kind, Type: kind, Enabled: true, DNS: []string{"1.1.1.1"}}); err == nil {
			t.Fatalf("%s silently ignored resolver setting", kind)
		}
	}
}

func TestDirectHostnameReportsConnectedDestinationIP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	provider, err := New(context.Background(), config.Outbound{ID: "direct", Type: "direct", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := provider.DialContext(ctx, "tcp", "localhost:"+port)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if got := c.(interface{ DestinationIP() string }).DestinationIP(); got != "127.0.0.1" {
		t.Fatalf("hostname connection reported %q", got)
	}
	accepted, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	_ = accepted.Close()
}

func TestTailscaleRouteGuardRejectsHostAndExitNodeFallback(t *testing.T) {
	routes := views.SliceOf([]netip.Prefix{netip.MustParsePrefix("192.168.50.0/24"), netip.MustParsePrefix("0.0.0.0/0")})
	st := &ipnstate.Status{Peer: map[key.NodePublic]*ipnstate.PeerStatus{{}: {TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.70.0.2")}, PrimaryRoutes: &routes}}}
	for _, tc := range []struct {
		ip   string
		want bool
	}{{"100.70.0.2", true}, {"192.168.50.42", true}, {"100.70.0.9", false}, {"1.1.1.1", false}, {"127.0.0.1", false}} {
		if got := routePermitted(st, netip.MustParseAddr(tc.ip)); got != tc.want {
			t.Fatalf("%s = %v", tc.ip, got)
		}
	}
}

func TestDNSAddressParser(t *testing.T) {
	name := dnsmessage.MustNewName("company.internal.")
	msg := dnsmessage.Message{Header: dnsmessage.Header{ID: 1, Response: true}, Answers: []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: name, Class: dnsmessage.ClassINET, Type: dnsmessage.TypeA}, Body: &dnsmessage.AResource{A: [4]byte{100, 70, 0, 2}}}}}
	b, e := msg.Pack()
	if e != nil {
		t.Fatal(e)
	}
	ips, e := dnsAddresses(b)
	if e != nil || len(ips) != 1 || ips[0].String() != "100.70.0.2" {
		t.Fatalf("%v %v", ips, e)
	}
	if _, e = dnsAddresses([]byte("malformed")); e == nil {
		t.Fatal("accepted malformed DNS")
	}
}
