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

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

type wireGuard struct {
	cfg       config.Outbound
	parsed    WGConfig
	device    *device.Device
	network   *netstack.Net
	mu        sync.RWMutex
	stopped   bool
	closeOnce sync.Once
}

func newWireGuard(ctx context.Context, cfg config.Outbound) (Provider, error) {
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
	// Only endpoint bootstrap uses the host resolver. Every destination lookup is
	// performed by the userspace tunnel resolver after the tunnel is constructed.
	var ipc strings.Builder
	fmt.Fprintf(&ipc, "private_key=%s\nlisten_port=%d\nreplace_peers=true\n", parsed.privateKey, parsed.listenPort)
	for _, p := range parsed.peers {
		host, port, _ := net.SplitHostPort(p.endpoint)
		if _, e := netip.ParseAddr(host); e != nil {
			ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if e != nil || len(ips) == 0 {
				return nil, errors.New("WireGuard endpoint bootstrap DNS failed")
			}
			host = ips[0].String()
		}
		fmt.Fprintf(&ipc, "public_key=%s\nendpoint=%s\npersistent_keepalive_interval=%d\nreplace_allowed_ips=true\n", p.publicKey, net.JoinHostPort(host, port), p.keepalive)
		if p.presharedKey != "" {
			fmt.Fprintf(&ipc, "preshared_key=%s\n", p.presharedKey)
		}
		for _, a := range p.allowed {
			fmt.Fprintf(&ipc, "allowed_ip=%s\n", a)
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
	return &wireGuard{cfg: cfg, parsed: parsed, device: dev, network: network}, nil
}
func (w *wireGuard) ID() string { return w.cfg.ID }
func (w *wireGuard) Close() error {
	w.closeOnce.Do(func() { w.mu.Lock(); w.stopped = true; w.mu.Unlock(); w.device.Close() })
	return nil
}

func (w *wireGuard) Status(context.Context) Status {
	w.mu.RLock()
	defer w.mu.RUnlock()
	state := "ready"
	if w.stopped {
		state = "stopped"
	}
	return Status{ID: w.ID(), Type: "wireguard", State: state, Detail: "userspace interface; handshake requires actual traffic", PublicInternet: w.cfg.PublicInternet}
}

func (w *wireGuard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := tcpNetwork(network); err != nil {
		return nil, err
	}
	w.mu.RLock()
	stopped := w.stopped
	w.mu.RUnlock()
	if stopped {
		return nil, errors.New("WireGuard is stopped")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	n, e := strconv.ParseUint(port, 10, 16)
	if e != nil || n == 0 {
		return nil, errors.New("invalid port")
	}
	var ips []netip.Addr
	if a, e := netip.ParseAddr(host); e == nil {
		ips = []netip.Addr{a}
	} else {
		if len(w.parsed.DNS) == 0 {
			return nil, errors.New("WireGuard profile has no tunnel DNS; host resolver fallback is disabled")
		}
		names, e := w.network.LookupContextHost(ctx, host)
		if e != nil {
			return nil, e
		}
		for _, s := range names {
			if a, e := netip.ParseAddr(s); e == nil {
				ips = append(ips, a)
			}
		}
	}
	var last error
	for _, a := range ips {
		a = a.Unmap()
		if !familyAllows(network, a) || !w.parsed.allows(a) {
			continue
		}
		c, e := w.network.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(a, uint16(n)))
		if e == nil {
			return &destinationConn{Conn: c, ip: a.String()}, nil
		}
		last = e
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
