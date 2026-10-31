package proxy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"rillway/internal/access"
	"rillway/internal/config"
	"rillway/internal/hostlocal"
	"sort"
	"strings"
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

func (g *ConnectionGuard) Capacity() access.ConnectionCapacity {
	g.mu.Lock()
	defer g.mu.Unlock()
	return access.ConnectionCapacity{
		ActiveConnections:       g.active,
		MaxConnections:          g.total,
		MaxConnectionsPerSource: g.perIP,
		SourceLimit:             g.rejected.SourceLimit,
		TotalLimit:              g.rejected.TotalLimit,
	}
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

type idleCandidate struct {
	addr netip.Addr
	seen time.Time
}

// evictIdleLocked removes oldest idle source records until len(g.sources) <= target,
// returning true if the target bound was achieved. Active connection rows are never evicted.
//
// Telemetry caveat: when connection limits are lowered, idle source records exceeding
// the new capacity bound are evicted (oldest last-seen first) to keep metadata storage
// bounded. Evicted idle records lose their per-source telemetry counters until the source
// reconnects, while active connections and guard-level cumulative rejection counters are preserved.
func (g *ConnectionGuard) evictIdleLocked(target int) bool {
	if len(g.sources) <= target {
		return true
	}
	excess := len(g.sources) - target
	if excess == 1 {
		var oldest netip.Addr
		var seen time.Time
		for addr, source := range g.sources {
			if source.ActiveConnections == 0 && (!oldest.IsValid() || source.LastSeen.Before(seen) || source.LastSeen.Equal(seen) && addr.Compare(oldest) < 0) {
				oldest = addr
				seen = source.LastSeen
			}
		}
		if !oldest.IsValid() {
			return false
		}
		delete(g.sources, oldest)
		return true
	}

	var idles []idleCandidate
	for addr, source := range g.sources {
		if source.ActiveConnections == 0 {
			idles = append(idles, idleCandidate{addr: addr, seen: source.LastSeen})
		}
	}
	if len(idles) == 0 {
		return false
	}
	if len(idles) <= excess {
		for _, item := range idles {
			delete(g.sources, item.addr)
		}
		return len(g.sources) <= target
	}
	sort.Slice(idles, func(i, j int) bool {
		if !idles[i].seen.Equal(idles[j].seen) {
			return idles[i].seen.Before(idles[j].seen)
		}
		return idles[i].addr.Compare(idles[j].addr) < 0
	})
	for i := range excess {
		delete(g.sources, idles[i].addr)
	}
	return true
}

func (g *ConnectionGuard) record(ip netip.Addr, now time.Time) *access.SourceClient {
	if source := g.sources[ip]; source != nil {
		source.LastSeen = now
		return source
	}
	limit := max(1024, g.total)
	if len(g.sources) >= limit {
		if !g.evictIdleLocked(limit - 1) {
			return nil
		}
	}
	source := &access.SourceClient{Address: ip.String(), LastSeen: now}
	g.sources[ip] = source
	return source
}

// ApplyConnectionLimits updates total and per-source connection capacity limits.
// It applies the effective limits (substituting zero values with defaults) under lock,
// preserves existing connections and cumulative counters, and evicts idle source
// metadata records if over the new capacity bound.
func (g *ConnectionGuard) ApplyConnectionLimits(limits config.ConnectionLimits) {
	eff := limits.Effective()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.total = eff.MaxConnections
	g.perIP = eff.MaxConnectionsPerSource
	g.evictIdleLocked(max(1024, g.total))
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
		case g.active >= g.total:
			g.rejected.TotalLimit++
			if source != nil {
				source.CapacityRejections++
				source.TotalLimitRejections++
			}
		case g.clients[ip] >= g.perIP:
			g.rejected.SourceLimit++
			if source != nil {
				source.CapacityRejections++
				source.SourceLimitRejections++
			}
		default:
			if source != nil {
				source.CapacityRejections++
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
	snapshot := access.SourceSnapshot{
		GeneratedAt: now,
		Capacity: access.ConnectionCapacity{
			ActiveConnections:       g.active,
			MaxConnections:          g.total,
			MaxConnectionsPerSource: g.perIP,
			SourceLimit:             g.rejected.SourceLimit,
			TotalLimit:              g.rejected.TotalLimit,
		},
		Clients: make([]access.SourceClient, 0, len(g.sources)),
	}
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

func (s *Server) Capacity() access.ConnectionCapacity { return s.guard.Capacity() }

// AdmissionRejections reports capacity rejections on the shared HTTP/SOCKS5 budget.
func (s *Server) AdmissionRejections() AdmissionRejections { return s.guard.Rejections() }

// ApplyConnectionLimits updates connection limits on the shared connection guard
// and the SOCKS5 UDP target budget derived from them.
func (s *Server) ApplyConnectionLimits(limits config.ConnectionLimits) {
	s.guard.ApplyConnectionLimits(limits)
	s.udp.setLimit(limits.Effective().MaxConnections)
}

// GuardListener registers a proxy endpoint for loop prevention and shares the
// configured HTTP/SOCKS admission budget across all guarded listeners.
func (s *Server) GuardListener(l net.Listener) net.Listener {
	if guarded, ok := l.(*guardedListener); ok && guarded.guard == s.guard {
		return l
	}
	if addr, err := netip.ParseAddrPort(l.Addr().String()); err == nil {
		s.updateEndpoints(func(e *localEndpoints) {
			e.proxy = append(e.proxy, netip.AddrPortFrom(addr.Addr().WithZone("").Unmap(), addr.Port()))
		})
	}
	return s.guard.Wrap(l)
}

// RefuseListener marks a management/PAC endpoint. The endpoint keeps its own
// ACL and authentication; connecting to it through the proxy is refused, so a
// proxy client cannot reach it from the host's own address.
func (s *Server) RefuseListener(l net.Listener) {
	if addr, err := netip.ParseAddrPort(l.Addr().String()); err == nil {
		s.updateEndpoints(func(e *localEndpoints) {
			e.internal = append(e.internal, netip.AddrPortFrom(addr.Addr().WithZone("").Unmap(), addr.Port()))
		})
	}
}

// Destination reasons returned by DestinationPolicy.
const (
	// DestinationProxyListener is this daemon's own HTTP/SOCKS5 listener: dialing
	// it would loop.
	DestinationProxyListener = "proxy_listener"
	// DestinationManagementEndpoint is the management or PAC listener, which only
	// its own ACL and authentication may expose.
	DestinationManagementEndpoint = "management_endpoint"
)

// DestinationPolicy returns why a destination must be refused, or "" when it may
// be dialed: only this daemon's own bound ports are refused. Other services on
// the same host remain reachable and observable. No host DNS lookup is performed.
func (s *Server) DestinationPolicy(address string) string {
	addr, err := netip.ParseAddrPort(address)
	if err != nil {
		return ""
	}
	ip := addr.Addr().WithZone("").Unmap()
	endpoints := s.endpoints.Load()
	for _, endpoint := range endpoints.proxy {
		if s.matchesEndpoint(endpoint, addr, ip) {
			return DestinationProxyListener
		}
	}
	for _, endpoint := range endpoints.internal {
		if s.matchesEndpoint(endpoint, addr, ip) {
			return DestinationManagementEndpoint
		}
	}
	return ""
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

var (
	errLocalDestination = errors.New("proxy destination is a local Rillway listener")
	errLocalPeer        = errors.New("proxy destination resolves to a local Rillway listener")
)

// localDestination reports this daemon's own proxy, management and PAC listeners. UDP also covers live
// SOCKS5 relay sockets: a datagram sent there would re-enter the relay.
func (s *Server) localDestination(network, address string) bool {
	return s.DestinationPolicy(address) != "" || strings.HasPrefix(network, "udp") && s.ownsUDPAddress(address)
}

// dialContext gives HTTP forwarding, CONNECT and SOCKS5 the same outbound
// budget. Tunnel providers rely on this context because they have no own timeout.
func (s *Server) dialContext(ctx context.Context, network, address string) (_ net.Conn, resultErr error) {
	if hostlocal.EventFrom(ctx) == nil {
		var event *hostlocal.Event
		ctx, event = s.beginHostEvent(ctx, address)
		defer func() { event.Complete(resultErr) }()
	}
	if s.localDestination(network, address) {
		s.countManagementRefusal(address)
		s.recordSelfRefusal(ctx, address)
		return nil, errLocalDestination
	}
	if err := s.checkHost(ctx, address, netip.AddrPort{}); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.dialTimeout)
	defer cancel()
	c, err := s.dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	// The selected outbound owns DNS. Check its actual connected peer instead
	// of doing a second host-DNS lookup that could expose private names.
	if s.localDestination(network, c.RemoteAddr().String()) {
		_ = c.Close()
		s.countManagementRefusal(c.RemoteAddr().String())
		s.recordSelfRefusal(ctx, c.RemoteAddr().String())
		return nil, errLocalPeer
	}
	return c, nil
}

// countManagementRefusal counts a refused attempt to reach the management or PAC
// listener. It records no address, so the counter can be shown to operators as is.
func (s *Server) countManagementRefusal(address string) {
	if s.DestinationPolicy(address) == DestinationManagementEndpoint {
		s.managementRefused.Add(1)
	}
}

// ManagementRefusals reports how many proxy requests for the management or PAC
// listener were refused since the daemon started (literal address or a name the
// selected outbound resolved to the listener). The counter resets on restart.
func (s *Server) ManagementRefusals() uint64 { return s.managementRefused.Load() }
