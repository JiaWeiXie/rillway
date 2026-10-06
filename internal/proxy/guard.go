package proxy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
)

// ConnectionGuard rejects excess or disallowed sockets before HTTP parsing,
// TLS handshakes or SOCKS workers allocate resources. A guard can be shared by
// multiple listeners, so changing protocols cannot evade its budget.
type ConnectionGuard struct {
	mu      sync.Mutex
	clients map[netip.Addr]int
	active  int
	total   int
	perIP   int
	allowed func(string) bool
}

func NewConnectionGuard(total, perIP int, allowed func(string) bool) *ConnectionGuard {
	return &ConnectionGuard{clients: make(map[netip.Addr]int), total: total, perIP: perIP, allowed: allowed}
}

func (g *ConnectionGuard) Wrap(l net.Listener) net.Listener {
	return &guardedListener{Listener: l, guard: g}
}

type guardedListener struct {
	net.Listener
	guard *ConnectionGuard
}

func (l *guardedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		peer, err := netip.ParseAddrPort(c.RemoteAddr().String())
		if err != nil || l.guard.allowed != nil && !l.guard.allowed(c.RemoteAddr().String()) {
			_ = c.Close()
			continue
		}
		ip := peer.Addr().Unmap()
		g := l.guard
		g.mu.Lock()
		accepted := g.active < g.total && g.clients[ip] < g.perIP
		if accepted {
			g.active++
			g.clients[ip]++
		}
		g.mu.Unlock()
		if !accepted {
			_ = c.Close()
			continue
		}
		return &guardedConn{Conn: c, release: func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.active--
			g.clients[ip]--
			if g.clients[ip] == 0 {
				delete(g.clients, ip)
			}
		}}, nil
	}
}

type guardedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *guardedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

func (c *guardedConn) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return c.Close()
}

// GuardListener registers a proxy endpoint for loop prevention and shares the
// HTTP/SOCKS admission budget: 256 sockets total, 64 per source IP.
func (s *Server) GuardListener(l net.Listener) net.Listener {
	if guarded, ok := l.(*guardedListener); ok && guarded.guard == s.guard {
		return l
	}
	if addr, err := netip.ParseAddrPort(l.Addr().String()); err == nil {
		s.mu.Lock()
		s.endpoints = append(s.endpoints, netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port()))
		s.mu.Unlock()
	}
	return s.guard.Wrap(l)
}

func (s *Server) ownEndpoint(address string) bool {
	blocked, _ := s.DestinationPolicy(address)
	return blocked
}

// IgnoreListener marks a management/PAC endpoint as internal traffic. It stays
// subject to its own ACL/authentication, but must not feed proxy observations.
func (s *Server) IgnoreListener(l net.Listener) {
	if addr, err := netip.ParseAddrPort(l.Addr().String()); err == nil {
		s.mu.Lock()
		s.internalEndpoints = append(s.internalEndpoints, netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port()))
		s.mu.Unlock()
	}
}

// DestinationPolicy identifies only this daemon's bound ports. Other services
// on the same host remain observable. No host DNS lookup is performed.
func (s *Server) DestinationPolicy(address string) (blocked, internal bool) {
	addr, err := netip.ParseAddrPort(address)
	if err != nil {
		return false, false
	}
	ip := addr.Addr().Unmap()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, endpoint := range s.endpoints {
		if s.matchesEndpoint(endpoint, addr, ip) {
			return true, false
		}
	}
	for _, endpoint := range s.internalEndpoints {
		if s.matchesEndpoint(endpoint, addr, ip) {
			return false, true
		}
	}
	return false, false
}

func (s *Server) matchesEndpoint(endpoint, target netip.AddrPort, ip netip.Addr) bool {
	if endpoint.Port() != target.Port() {
		return false
	}
	if endpoint.Addr() == ip {
		return true
	}
	// An IPv4 wildcard cannot cover a distinct IPv6 listener on the same port.
	return endpoint.Addr().IsUnspecified() && (!endpoint.Addr().Is4() || ip.Is4()) && (s.localIPs[ip] || ip.IsLoopback())
}

func (s *Server) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if s.ownEndpoint(address) {
		return nil, errors.New("proxy destination is a local proxy listener")
	}
	c, err := s.dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	// The selected outbound owns DNS. Check its actual connected peer instead
	// of doing a second host-DNS lookup that could expose private names.
	if s.ownEndpoint(c.RemoteAddr().String()) {
		_ = c.Close()
		return nil, errors.New("proxy destination resolves to a local proxy listener")
	}
	return c, nil
}
