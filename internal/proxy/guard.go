package proxy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"rillway/internal/access"
	"sort"
	"sync"
	"time"
)

// ConnectionGuard rejects excess or disallowed sockets before HTTP parsing,
// TLS handshakes or SOCKS workers allocate resources. A guard can be shared by
// multiple listeners, so changing protocols cannot evade its budget.
type ConnectionGuard struct {
	mu                   sync.Mutex
	clients              map[netip.Addr]int
	ingress              map[*guardedConn]netip.Addr
	sources              map[netip.Addr]*access.SourceClient
	active, total, perIP int
	decide               func(netip.Addr) access.Decision
	rejected             AdmissionRejections
	now                  func() time.Time
}

// AdmissionRejections counts allowed-source sockets closed for capacity.
type AdmissionRejections struct {
	SourceLimit uint64 `json:"source_limit"`
	TotalLimit  uint64 `json:"total_limit"`
}

func NewConnectionGuard(total, perIP int, decide func(netip.Addr) access.Decision) *ConnectionGuard {
	return &ConnectionGuard{clients: make(map[netip.Addr]int), ingress: make(map[*guardedConn]netip.Addr), sources: make(map[netip.Addr]*access.SourceClient), total: total, perIP: perIP, decide: decide, now: time.Now}
}

func (g *ConnectionGuard) Rejections() AdmissionRejections {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.rejected
}

func (g *ConnectionGuard) Wrap(l net.Listener) net.Listener {
	return &guardedListener{Listener: l, guard: g}
}

type guardedListener struct {
	net.Listener
	guard *ConnectionGuard
}

func (g *ConnectionGuard) decision(ip netip.Addr) access.Decision {
	if g.decide == nil {
		return access.Decision{Allowed: true}
	}
	return g.decide(ip)
}

func (g *ConnectionGuard) prune(now time.Time) {
	for ip, source := range g.sources {
		if source.ActiveConnections == 0 && now.Sub(source.LastSeen) >= 24*time.Hour {
			delete(g.sources, ip)
		}
	}
}

func (g *ConnectionGuard) record(ip netip.Addr, now time.Time) *access.SourceClient {
	if source := g.sources[ip]; source != nil {
		source.LastSeen = now
		return source
	}
	if len(g.sources) >= 1024 {
		var oldest netip.Addr
		var seen time.Time
		for addr, source := range g.sources {
			if source.ActiveConnections == 0 && (!oldest.IsValid() || source.LastSeen.Before(seen) || source.LastSeen.Equal(seen) && addr.Compare(oldest) < 0) {
				oldest = addr
				seen = source.LastSeen
			}
		}
		if !oldest.IsValid() {
			return nil
		}
		delete(g.sources, oldest)
	}
	source := &access.SourceClient{Address: ip.String(), LastSeen: now}
	g.sources[ip] = source
	return source
}

func (l *guardedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip, err := access.ParsePeer(c.RemoteAddr().String())
		if err != nil {
			_ = c.Close()
			continue
		}
		g := l.guard
		g.mu.Lock()
		now := g.now().UTC()
		g.prune(now)
		source := g.record(ip, now)
		decision := g.decision(ip)
		accepted := decision.Allowed && source != nil && g.active < g.total && g.clients[ip] < g.perIP
		switch {
		case !decision.Allowed:
			if source != nil {
				source.DeniedConnections++
			}
		case accepted:
			g.active++
			g.clients[ip]++
			source.ActiveConnections++
			source.AcceptedConnections++
		default:
			if source != nil {
				source.CapacityRejections++
			}
			if g.active >= g.total {
				g.rejected.TotalLimit++
			} else {
				g.rejected.SourceLimit++
			}
		}
		if !accepted {
			g.mu.Unlock()
			_ = c.Close()
			continue
		}
		wrapped := &guardedConn{Conn: c}
		wrapped.release = func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			delete(g.ingress, wrapped)
			g.active--
			g.clients[ip]--
			source.ActiveConnections--
			if g.clients[ip] == 0 {
				delete(g.clients, ip)
			}
		}
		g.ingress[wrapped] = ip
		g.mu.Unlock()
		return wrapped, nil
	}
}

func (g *ConnectionGuard) SourceClients() access.SourceSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now().UTC()
	g.prune(now)
	snapshot := access.SourceSnapshot{GeneratedAt: now, Clients: make([]access.SourceClient, 0, len(g.sources))}
	for ip, source := range g.sources {
		row := *source
		d := g.decision(ip)
		row.Decision = "deny"
		if d.Allowed {
			row.Decision = "allow"
		}
		row.RuleID = d.RuleID
		snapshot.Clients = append(snapshot.Clients, row)
	}
	sort.Slice(snapshot.Clients, func(i, j int) bool {
		a, b := snapshot.Clients[i], snapshot.Clients[j]
		if (a.ActiveConnections > 0) != (b.ActiveConnections > 0) {
			return a.ActiveConnections > 0
		}
		if !a.LastSeen.Equal(b.LastSeen) {
			return a.LastSeen.After(b.LastSeen)
		}
		return a.Address < b.Address
	})
	return snapshot
}

func (g *ConnectionGuard) DisconnectSource(ip netip.Addr) int {
	ip = ip.WithZone("").Unmap()
	g.mu.Lock()
	matches := make([]*guardedConn, 0, g.clients[ip])
	for c, addr := range g.ingress {
		if addr == ip {
			matches = append(matches, c)
		}
	}
	g.mu.Unlock()
	count := 0
	for _, c := range matches {
		if closed, _ := c.closeOnce(); closed {
			count++
		}
	}
	return count
}

type guardedConn struct {
	net.Conn
	once     sync.Once
	release  func()
	closeErr error
}

func (c *guardedConn) closeOnce() (bool, error) {
	closed := false
	c.once.Do(func() { closed = true; c.closeErr = c.Conn.Close(); c.release() })
	return closed, c.closeErr
}
func (c *guardedConn) Close() error { _, err := c.closeOnce(); return err }
func (c *guardedConn) CloseWrite() error {
	if half, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return half.CloseWrite()
	}
	return c.Close()
}

func (s *Server) SourceClients() access.SourceSnapshot  { return s.guard.SourceClients() }
func (s *Server) DisconnectSource(ip netip.Addr) int    { return s.guard.DisconnectSource(ip) }
func (s *Server) SetSourcePolicy(policy *access.Policy) { s.sourcePolicy.Store(policy) }

// AdmissionRejections reports capacity rejections on the shared HTTP/SOCKS5 budget.
func (s *Server) AdmissionRejections() AdmissionRejections { return s.guard.Rejections() }

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

// dialContext gives HTTP forwarding, CONNECT and SOCKS5 the same outbound
// budget. Tunnel providers rely on this context because they have no own timeout.
func (s *Server) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if s.ownEndpoint(address) {
		return nil, errors.New("proxy destination is a local proxy listener")
	}
	ctx, cancel := context.WithTimeout(ctx, s.dialTimeout)
	defer cancel()
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
