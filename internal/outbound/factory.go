package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"rillway/internal/config"
	"strconv"
	"time"
)

// New constructs an outbound. WARP construction is deliberately read-only; the
// user must explicitly connect it. Embedded VPNs start only when enabled.
func New(ctx context.Context, cfg config.Outbound) (Provider, error) {
	if !cfg.Enabled {
		return nil, fmt.Errorf("outbound %q is disabled", cfg.ID)
	}
	switch cfg.Type {
	case "direct":
		if len(cfg.DNS) != 0 {
			return nil, errors.New("direct uses the host resolver; per-outbound DNS is only supported by WireGuard and Tailscale")
		}
		return &direct{id: cfg.ID}, nil
	case "warp":
		if len(cfg.DNS) != 0 {
			return nil, config.PublicError{Message: "WARP resolves DNS through its official proxy. Clear the custom DNS field for this outbound."}
		}
		if cfg.ProxyAddress != "" {
			host, port, err := net.SplitHostPort(cfg.ProxyAddress)
			n, numberErr := strconv.Atoi(port)
			if err != nil || host != "127.0.0.1" || numberErr != nil || n < 1 || n > 65535 {
				return nil, config.PublicError{Message: "WARP Local Proxy must use 127.0.0.1 with a port from 1 to 65535."}
			}
		}
		return newWARP(cfg), nil
	case "wireguard":
		return newWireGuard(ctx, cfg)
	case "tailscale":
		return newTailscale(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported outbound type %q", cfg.Type)
	}
}

func tcpNetwork(network string) error {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return errors.New("only tcp, tcp4 and tcp6 are supported")
	}
	return nil
}

func familyAllows(network string, addr netip.Addr) bool {
	addr = addr.Unmap()
	return network == "tcp" || network == "tcp4" && addr.Is4() || network == "tcp6" && addr.Is6()
}

type direct struct{ id string }

func (d *direct) ID() string   { return d.id }
func (d *direct) Close() error { return nil }
func (d *direct) Status(context.Context) Status {
	return Status{ID: d.id, Type: "direct", State: "ready", PublicInternet: true}
}

func (d *direct) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := tcpNetwork(network); err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	connection, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	ip := ""
	if host, _, err := net.SplitHostPort(connection.RemoteAddr().String()); err == nil {
		if addr, err := netip.ParseAddr(host); err == nil {
			ip = addr.Unmap().String()
		}
	}
	return &destinationConn{Conn: connection, ip: ip}, nil
}

type destinationConn struct {
	net.Conn
	ip string
}

func (c *destinationConn) DestinationIP() string { return c.ip }

func (c *destinationConn) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return c.Close()
}
