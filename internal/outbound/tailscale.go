package outbound

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"rillway/internal/config"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

type tailscale struct {
	cfg      config.Outbound
	server   *tsnet.Server
	mu       sync.RWMutex
	actionMu sync.Mutex
	stopped  bool
}

func newTailscale(parent context.Context, cfg config.Outbound) (Provider, error) {
	for _, resolver := range cfg.DNS {
		if _, err := netip.ParseAddr(resolver); err != nil {
			return nil, errors.New("tailscale DNS resolvers must be literal tunnel IP addresses")
		}
	}
	if cfg.PublicInternet {
		return nil, errors.New("tailscale public internet requires an exit-node feature, which is not enabled; use tailnet/subnet destinations")
	}
	if cfg.StateDir == "" {
		return nil, errors.New("tailscale requires a dedicated state_dir")
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Stat(cfg.StateDir); err != nil || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("tailscale state_dir must have mode 0700")
	}
	auth := ""
	if cfg.AuthKeyFile != "" {
		info, e := os.Stat(cfg.AuthKeyFile)
		if e != nil {
			return nil, e
		}
		if info.Mode().Perm()&0o077 != 0 || info.Size() > 4096 {
			return nil, errors.New("tailscale auth key file must have mode 0600 and at most 4096 bytes")
		}
		b, e := os.ReadFile(cfg.AuthKeyFile)
		if e != nil {
			return nil, e
		}
		auth = strings.TrimSpace(string(b))
	}
	s := &tsnet.Server{Dir: cfg.StateDir, Hostname: cfg.Hostname, AuthKey: auth, Logf: func(string, ...any) {}, UserLogf: func(string, ...any) {}}
	// Start only our embedded node, never the machine's existing tailscaled.
	if err := s.Start(); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("embedded Tailscale: %w", err)
	}
	lc, err := s.LocalClient()
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	// These routes are inside tsnet's userspace stack; no host routes change.
	if _, err = lc.EditPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{RouteAll: true}, RouteAllSet: true}); err != nil {
		_ = s.Close()
		return nil, err
	}
	return &tailscale{cfg: cfg, server: s}, nil
}
func (t *tailscale) ID() string   { return t.cfg.ID }
func (t *tailscale) Close() error { return t.server.Close() }
func (t *tailscale) Status(ctx context.Context) Status {
	s := Status{ID: t.ID(), Type: "tailscale", State: "unavailable", PublicInternet: false}
	t.mu.RLock()
	stopped := t.stopped
	t.mu.RUnlock()
	if stopped {
		s.State = "stopped"
		return s
	}
	lc, e := t.server.LocalClient()
	if e != nil {
		s.Detail = e.Error()
		return s
	}
	raw, e := lc.Status(ctx)
	if e != nil {
		s.Detail = e.Error()
		return s
	}
	s.State = raw.BackendState
	s.AuthURL = raw.AuthURL
	s.Detail = "embedded node; only advertised tailnet and subnet routes permitted"
	return s
}

// routePermitted excludes default routes, even if a peer advertises an exit
// node. tsnet's UserDial may use the host network for unowned destinations, so
// validating the selected address before calling it is essential.
func routePermitted(st *ipnstate.Status, addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, a := range st.TailscaleIPs {
		if a == addr {
			return true
		}
	}
	for _, p := range st.Peer {
		for _, a := range p.TailscaleIPs {
			if a == addr {
				return true
			}
		}
		if p.PrimaryRoutes != nil {
			for _, r := range p.PrimaryRoutes.All() {
				if r.Bits() > 0 && r.Contains(addr) {
					return true
				}
			}
		}
	}
	return false
}

func (t *tailscale) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := tcpNetwork(network); err != nil {
		return nil, err
	}
	t.mu.RLock()
	stopped := t.stopped
	t.mu.RUnlock()
	if stopped {
		return nil, errors.New("tailscale was manually stopped")
	}
	lc, err := t.server.LocalClient()
	if err != nil {
		return nil, err
	}
	st, err := lc.Status(ctx)
	if err != nil {
		return nil, err
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips := []netip.Addr{}
	if ip, e := netip.ParseAddr(host); e == nil {
		ips = append(ips, ip)
	} else {
		suffix := ""
		if st.CurrentTailnet != nil {
			suffix = strings.TrimSuffix(strings.ToLower(st.CurrentTailnet.MagicDNSSuffix), ".")
		}
		host = strings.TrimSuffix(strings.ToLower(host), ".")
		if !strings.Contains(host, ".") && suffix != "" {
			host += "." + suffix
		}
		magic := suffix != "" && (host == suffix || strings.HasSuffix(host, "."+suffix))
		for _, qt := range []struct {
			name string
			typ  dnsmessage.Type
		}{{"A", dnsmessage.TypeA}, {"AAAA", dnsmessage.TypeAAAA}} {
			if network == "tcp4" && qt.typ == dnsmessage.TypeAAAA || network == "tcp6" && qt.typ == dnsmessage.TypeA {
				continue
			}
			var packet []byte
			if magic {
				packet, _, err = lc.QueryDNS(ctx, host+".", qt.name)
			} else {
				// Non-MagicDNS private names require explicit DNS routed inside the
				// tailnet. Never ask QueryDNS to fall back to the host/public resolver.
				if len(t.cfg.DNS) == 0 {
					return nil, errors.New("non-MagicDNS names require explicit Tailscale tunnel DNS; public fallback is disabled")
				}
				err = errors.New("no reachable routed Tailscale DNS")
				for _, server := range t.cfg.DNS {
					ip, e := netip.ParseAddr(server)
					if e != nil || !routePermitted(st, ip) {
						continue
					}
					packet, e = t.queryDNS(ctx, ip, host, qt.typ)
					if e == nil {
						err = nil
						break
					}
				}
			}
			if err != nil {
				continue
			}
			answers, e := dnsAddresses(packet)
			if e == nil {
				ips = append(ips, answers...)
			}
		}
	}
	var last error
	for _, ip := range ips {
		ip = ip.Unmap()
		if !familyAllows(network, ip) || !routePermitted(st, ip) {
			continue
		}
		c, e := t.server.Dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if e == nil {
			return &destinationConn{Conn: c, ip: ip.String()}, nil
		}
		last = e
	}
	if last == nil {
		last = errors.New("no matching destination within active Tailscale peer/subnet routes (host fallback disabled)")
	}
	return nil, last
}

func (t *tailscale) queryDNS(ctx context.Context, server netip.Addr, host string, typ dnsmessage.Type) ([]byte, error) {
	name, err := dnsmessage.NewName(host + ".")
	if err != nil {
		return nil, err
	}
	request := dnsmessage.Message{Header: dnsmessage.Header{ID: 1, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: name, Type: typ, Class: dnsmessage.ClassINET}}}
	packet, err := request.Pack()
	if err != nil {
		return nil, err
	}
	conn, err := t.server.Dial(ctx, "tcp", net.JoinHostPort(server.String(), "53"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(5 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	buf := binary.BigEndian.AppendUint16(nil, uint16(len(packet)))
	buf = append(buf, packet...)
	if _, err = conn.Write(buf); err != nil {
		return nil, err
	}
	var size [2]byte
	if _, err = io.ReadFull(conn, size[:]); err != nil {
		return nil, err
	}
	response := make([]byte, binary.BigEndian.Uint16(size[:]))
	_, err = io.ReadFull(conn, response)
	return response, err
}

func dnsAddresses(packet []byte) ([]netip.Addr, error) {
	var msg dnsmessage.Message
	if err := msg.Unpack(packet); err != nil {
		return nil, err
	}
	if !msg.Response || msg.RCode != dnsmessage.RCodeSuccess {
		return nil, errors.New("DNS query failed")
	}
	var result []netip.Addr
	for _, r := range msg.Answers {
		switch a := r.Body.(type) {
		case *dnsmessage.AResource:
			result = append(result, netip.AddrFrom4(a.A))
		case *dnsmessage.AAAAResource:
			result = append(result, netip.AddrFrom16(a.AAAA))
		}
	}
	return result, nil
}

func (t *tailscale) Action(ctx context.Context, action, value string) error {
	t.actionMu.Lock()
	defer t.actionMu.Unlock()
	lc, err := t.server.LocalClient()
	if err != nil {
		return err
	}
	switch action {
	case "login":
		return lc.StartLoginInteractive(ctx)
	case "logout":
		if err := lc.Logout(ctx); err != nil {
			return err
		}
		t.mu.Lock()
		t.stopped = true
		t.mu.Unlock()
		return nil
	case "connect", "disconnect":
		running := action == "connect"
		_, err = lc.EditPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{WantRunning: running}, WantRunningSet: true})
		if err == nil {
			t.mu.Lock()
			t.stopped = !running
			t.mu.Unlock()
		}
		return err
	default:
		return fmt.Errorf("unsupported Tailscale action %q", action)
	}
}
