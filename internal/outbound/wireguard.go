package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"rillway/internal/config"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

const (
	wgRefreshInterval = 30 * time.Second
	wgStaleHandshake  = 135 * time.Second
	wgResolveTimeout  = 5 * time.Second
	wgResolveBackoff  = 5 * time.Second
)

type (
	endpointResolver func(context.Context, string) ([]netip.Addr, error)
	wgEndpoint       struct {
		publicKey   string
		host        string
		port        uint16
		ips         []netip.Addr
		index       int
		configured  bool
		pending     bool
		lastAttempt time.Time
	}
)

type wireGuard struct {
	cfg       config.Outbound
	parsed    WGConfig
	device    *device.Device
	network   *netstack.Net
	resolve   endpointResolver
	endpoints []wgEndpoint

	mu             sync.RWMutex
	stopped        bool
	unresolved     int
	dials          atomic.Uint64
	lastDials      uint64
	refreshMu      sync.Mutex
	wake           chan struct{}
	refreshContext context.Context
	cancelRefresh  context.CancelFunc
	done           chan struct{}
	closeOnce      sync.Once
}

func newWireGuard(ctx context.Context, cfg config.Outbound) (Provider, error) {
	w, err := newWireGuardWith(ctx, cfg, lookupEndpoint, wgRefreshInterval)
	if err != nil {
		return nil, err
	}
	return w, nil
}

func newWireGuardWith(ctx context.Context, cfg config.Outbound, resolve endpointResolver, interval time.Duration) (*wireGuard, error) {
	info, err := os.Stat(cfg.ConfigFile)
	if err != nil {
		return nil, fmt.Errorf("WireGuard config: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("WireGuard config contains a private key and must have mode 0600")
	}
	if info.Size() > 1<<20 {
		return nil, errors.New("WireGuard config exceeds 1 MiB")
	}
	b, err := os.ReadFile(cfg.ConfigFile)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseWireGuard(string(b))
	if err != nil {
		return nil, err
	}
	if len(cfg.DNS) > 0 {
		parsed.DNS = nil
		for _, s := range cfg.DNS {
			a, e := netip.ParseAddr(s)
			if e != nil || !parsed.allows(a) {
				return nil, errors.New("WireGuard override DNS must be a literal IP within AllowedIPs")
			}
			parsed.DNS = append(parsed.DNS, a)
		}
	}

	var ipc strings.Builder
	fmt.Fprintf(&ipc, "private_key=%s\nlisten_port=%d\nreplace_peers=true\n", parsed.privateKey, parsed.listenPort)
	endpoints := make([]wgEndpoint, 0, len(parsed.peers))
	for _, peer := range parsed.peers {
		host, port, _ := net.SplitHostPort(peer.endpoint)
		fmt.Fprintf(&ipc, "public_key=%s\n", peer.publicKey)
		if ip, err := netip.ParseAddr(host); err == nil {
			fmt.Fprintf(&ipc, "endpoint=%s\n", netip.AddrPortFrom(ip.Unmap(), uint16Port(port)))
		} else {
			endpoint := wgEndpoint{publicKey: peer.publicKey, host: host, port: uint16Port(port)}
			resolveCtx, cancel := context.WithTimeout(ctx, wgResolveTimeout)
			ips, resolveErr := resolve(resolveCtx, host)
			cancel()
			if resolveErr == nil {
				endpoint.ips = normalizeEndpointIPs(ips)
			}
			if len(endpoint.ips) > 0 {
				fmt.Fprintf(&ipc, "endpoint=%s\n", netip.AddrPortFrom(endpoint.ips[0], endpoint.port))
				endpoint.configured = true
			}
			endpoints = append(endpoints, endpoint)
		}
		fmt.Fprintf(&ipc, "persistent_keepalive_interval=%d\nreplace_allowed_ips=true\n", peer.keepalive)
		if peer.presharedKey != "" {
			fmt.Fprintf(&ipc, "preshared_key=%s\n", peer.presharedKey)
		}
		for _, allowed := range peer.allowed {
			fmt.Fprintf(&ipc, "allowed_ip=%s\n", allowed)
		}
	}
	tun, network, err := netstack.CreateNetTUN(parsed.Addresses, parsed.DNS, parsed.MTU)
	if err != nil {
		return nil, fmt.Errorf("WireGuard userspace network: %w", err)
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	if err = dev.IpcSet(ipc.String()); err != nil {
		dev.Close()
		return nil, errors.New("WireGuard device rejected configuration")
	}
	if err = dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("WireGuard start: %w", err)
	}
	refreshCtx, cancelRefresh := context.WithCancel(context.Background())
	w := &wireGuard{cfg: cfg, parsed: parsed, device: dev, network: network, resolve: resolve, endpoints: endpoints, wake: make(chan struct{}, 1), done: make(chan struct{}), refreshContext: refreshCtx, cancelRefresh: cancelRefresh}
	w.recountUnresolved()
	if len(endpoints) > 0 {
		go w.refreshLoop(interval)
	}
	return w, nil
}

func lookupEndpoint(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	ips = normalizeEndpointIPs(ips)
	if len(ips) == 0 {
		return nil, errors.New("no endpoint addresses")
	}
	return ips, nil
}

func normalizeEndpointIPs(ips []netip.Addr) []netip.Addr {
	seen := make(map[netip.Addr]struct{}, len(ips))
	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		ip = ip.Unmap()
		if !ip.IsValid() {
			continue
		}
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		out = append(out, ip)
	}
	return out
}

func uint16Port(port string) uint16 {
	value, _ := strconv.ParseUint(port, 10, 16)
	return uint16(value)
}

func (w *wireGuard) ID() string { return w.cfg.ID }

func (w *wireGuard) Close() error {
	w.closeOnce.Do(func() {
		close(w.done)
		w.cancelRefresh()
		w.mu.Lock()
		w.stopped = true
		w.mu.Unlock()
		w.device.Close()
	})
	return nil
}

func (w *wireGuard) refreshLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-w.wake:
		case <-ticker.C:
		}
		ctx, cancel := context.WithTimeout(w.refreshContext, wgResolveTimeout)
		w.refreshEndpoints(ctx, time.Now())
		cancel()
	}
}

func (w *wireGuard) refreshEndpoints(ctx context.Context, now time.Time) {
	w.refreshMu.Lock()
	defer w.refreshMu.Unlock()
	status, err := w.device.IpcGet()
	if err != nil {
		return
	}
	handshakes := wgHandshakes(status)
	dials := w.dials.Load()
	attempted := dials != w.lastDials
	w.lastDials = dials
	for i := range w.endpoints {
		endpoint := &w.endpoints[i]
		if endpoint.pending {
			if w.applyEndpoint(endpoint) {
				endpoint.pending = false
				endpoint.configured = true
			}
			continue
		}
		if endpoint.ips == nil {
			if !endpoint.lastAttempt.IsZero() && now.Sub(endpoint.lastAttempt) < wgResolveBackoff {
				continue
			}
			endpoint.lastAttempt = now
			ips, err := w.resolve(ctx, endpoint.host)
			ips = normalizeEndpointIPs(ips)
			if err != nil || len(ips) == 0 {
				continue
			}
			endpoint.ips, endpoint.index, endpoint.pending = ips, 0, true
		} else {
			sec := handshakes[endpoint.publicKey]
			stale := sec == 0 || now.Sub(time.Unix(sec, 0)) > wgStaleHandshake
			if !stale || !attempted {
				continue
			}
			endpoint.lastAttempt = now
			ips, err := w.resolve(ctx, endpoint.host)
			ips = normalizeEndpointIPs(ips)
			if err == nil && len(ips) > 0 && !sameEndpointSet(endpoint.ips, ips) {
				endpoint.ips, endpoint.index, endpoint.pending = ips, 0, true
			} else if len(endpoint.ips) > 1 {
				endpoint.index = (endpoint.index + 1) % len(endpoint.ips)
				endpoint.pending = true
			} else {
				continue
			}
		}
		if w.applyEndpoint(endpoint) {
			endpoint.pending = false
			endpoint.configured = true
		}
	}
	w.recountUnresolved()
}

func (w *wireGuard) applyEndpoint(endpoint *wgEndpoint) bool {
	return w.device.IpcSet(fmt.Sprintf("public_key=%s\nupdate_only=true\nendpoint=%s\n", endpoint.publicKey, netip.AddrPortFrom(endpoint.ips[endpoint.index], endpoint.port))) == nil
}

func (w *wireGuard) recountUnresolved() {
	unresolved := 0
	for _, endpoint := range w.endpoints {
		if !endpoint.configured {
			unresolved++
		}
	}
	w.mu.Lock()
	w.unresolved = unresolved
	w.mu.Unlock()
}

func wgHandshakes(status string) map[string]int64 {
	out := map[string]int64{}
	key := ""
	for _, line := range strings.Split(status, "\n") {
		if value, ok := strings.CutPrefix(line, "public_key="); ok {
			key = value
		} else if value, ok := strings.CutPrefix(line, "last_handshake_time_sec="); ok && key != "" {
			out[key], _ = strconv.ParseInt(value, 10, 64)
		}
	}
	return out
}

func sameEndpointSet(a, b []netip.Addr) bool {
	if len(a) != len(b) {
		return false
	}
	for _, ip := range a {
		found := false
		for _, other := range b {
			if ip == other {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (w *wireGuard) Status(context.Context) Status {
	w.mu.RLock()
	defer w.mu.RUnlock()
	state, detail := "ready", "userspace interface; handshake requires actual traffic"
	if w.stopped {
		state = "stopped"
	} else if w.unresolved > 0 {
		state, detail = "unavailable", "WireGuard endpoint bootstrap DNS failed"
	}
	return Status{ID: w.ID(), Type: "wireguard", State: state, Detail: detail, PublicInternet: w.cfg.PublicInternet}
}

func (w *wireGuard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := tcpNetwork(network); err != nil {
		return nil, err
	}
	w.mu.RLock()
	stopped, unresolved := w.stopped, w.unresolved
	w.mu.RUnlock()
	if stopped {
		return nil, errors.New("WireGuard is stopped")
	}
	w.dials.Add(1)
	if unresolved > 0 {
		select {
		case w.wake <- struct{}{}:
		default:
		}
		if unresolved == len(w.parsed.peers) {
			return nil, errors.New("WireGuard endpoint bootstrap DNS failed")
		}
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return nil, errors.New("invalid port")
	}
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		if len(w.parsed.DNS) == 0 {
			return nil, errors.New("WireGuard profile has no tunnel DNS; host resolver fallback is disabled")
		}
		names, err := w.network.LookupContextHost(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if ip, err := netip.ParseAddr(name); err == nil {
				ips = append(ips, ip)
			}
		}
	}
	var last error
	for _, ip := range ips {
		ip = ip.Unmap()
		if !familyAllows(network, ip) || !w.parsed.allows(ip) {
			continue
		}
		conn, err := w.network.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(ip, uint16(n)))
		if err == nil {
			return &destinationConn{Conn: conn, ip: ip.String()}, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("destination has no address matching requested family and WireGuard AllowedIPs")
	}
	return nil, last
}

func (w *wireGuard) Action(ctx context.Context, action, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	switch action {
	case "connect":
		if err := w.device.Up(); err != nil {
			return err
		}
		w.stopped = false
		return nil
	case "disconnect":
		w.stopped = true
		return w.device.Down()
	default:
		return fmt.Errorf("unsupported WireGuard action %q", action)
	}
}
