package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"rillway/internal/routeinfo"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	socksUDPMaxTargets   = 32
	socksUDPIdleTimeout  = 2 * time.Minute
	socksUDPWriteTimeout = 5 * time.Second
	socksUDPMaxPacket    = 65535
	socksUDPRetryDelay   = 5 * time.Second
	// Live UDP targets per admitted proxy connection; follows max_connections.
	socksUDPTargetsPerConnection = 4
)

// UDPDrops counts SOCKS5 UDP datagrams the relay dropped before any outbound
// saw them. Dial failures are counted per outbound by the engine.
type UDPDrops struct {
	TargetLimit      uint64 `json:"target_limit"`      // global live-target budget exhausted
	RecentFailure    uint64 `json:"recent_failure"`    // destination failed within the retry delay
	LocalDestination uint64 `json:"local_destination"` // own listener or relay socket
}

// udpRelay is shared by all associations. The target budget bounds sockets and
// reader goroutines across clients; each association holds at most
// socksUDPMaxTargets. A target map entry owns one slot until it is removed.
type udpRelay struct {
	limit, targets                               atomic.Int64
	targetLimit, recentFailure, localDestination atomic.Uint64
	routes                                       atomic.Uint64 // bumped per applied configuration
	retryDelay                                   time.Duration
}

func (r *udpRelay) setLimit(maxConnections int) {
	r.limit.Store(int64(maxConnections) * socksUDPTargetsPerConnection)
}

// acquire reserves a target slot. A lowered limit applies as targets close.
func (r *udpRelay) acquire() bool {
	for {
		n := r.targets.Load()
		if n >= r.limit.Load() {
			r.targetLimit.Add(1)
			return false
		}
		if r.targets.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

func (r *udpRelay) release() { r.targets.Add(-1) }

// UDPDrops reports datagrams dropped by the SOCKS5 UDP relay.
func (s *Server) UDPDrops() UDPDrops {
	return UDPDrops{TargetLimit: s.udp.targetLimit.Load(), RecentFailure: s.udp.recentFailure.Load(), LocalDestination: s.udp.localDestination.Load()}
}

// ResetUDPRetryBackoff lets associations redial destinations that failed under
// an earlier configuration: the new routes may reach them. Counters are kept.
func (s *Server) ResetUDPRetryBackoff() { s.udp.routes.Add(1) }

type socksUDPTarget struct {
	address  string
	header   []byte
	conn     net.Conn
	lastUsed time.Time // protected by the association mutex
}

type socksUDPAssociation struct {
	server      *Server
	control     net.Conn
	listener    *net.UDPConn
	ctx         context.Context
	sourceIP    netip.Addr
	client      netip.AddrPort
	idleTimeout time.Duration

	mu            sync.Mutex
	lastActivity  time.Time
	targets       map[string]*socksUDPTarget
	failures      map[string]time.Time // destination -> earliest redial; bounded like targets
	failureRoutes uint64               // relay route generation of failures
	closed        bool
	workers       sync.WaitGroup
}

// The requested address can only identify the socket that will send datagrams.
// A hostname cannot be compared to the already authenticated TCP peer without
// resolving it on the host, so reject it rather than weakening source pinning.
func socksAssociatePort(r io.Reader, kind byte, source netip.Addr) (uint16, error) {
	var requested netip.Addr
	switch kind {
	case 1:
		var ip [4]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			return 0, err
		}
		requested = netip.AddrFrom4(ip)
	case 4:
		var ip [16]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			return 0, err
		}
		requested = netip.AddrFrom16(ip).Unmap()
	default:
		return 0, errors.New("unsupported SOCKS UDP client address")
	}
	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return 0, err
	}
	if !requested.IsUnspecified() && requested != source {
		return 0, errors.New("SOCKS UDP client address differs from TCP peer")
	}
	return binary.BigEndian.Uint16(port[:]), nil
}

func socksUDPBoundReply(w io.Writer, address *net.UDPAddr) bool {
	ip := address.IP.To4()
	kind := byte(1)
	if ip == nil {
		ip = address.IP.To16()
		kind = 4
	}
	if ip == nil {
		return false
	}
	var reply [22]byte
	reply[0], reply[1], reply[3] = 5, 0, kind
	copy(reply[4:], ip)
	length := 4 + len(ip)
	binary.BigEndian.PutUint16(reply[length:], uint16(address.Port))
	n, err := w.Write(reply[:length+2])
	return err == nil && n == length+2
}

func (s *Server) socksAssociate(ctx context.Context, control net.Conn, kind byte) {
	remote, remoteOK := control.RemoteAddr().(*net.TCPAddr)
	local, localOK := control.LocalAddr().(*net.TCPAddr)
	if !remoteOK || !localOK {
		socksReply(control, 1)
		return
	}
	sourceIP, valid := netip.AddrFromSlice(remote.IP)
	if !valid || sourceIP.IsUnspecified() {
		socksReply(control, 1)
		return
	}
	bindIP, valid := netip.AddrFromSlice(local.IP)
	if !valid || bindIP.IsUnspecified() {
		socksReply(control, 1)
		return
	}
	sourceIP = sourceIP.WithZone("").Unmap()
	port, err := socksAssociatePort(control, kind, sourceIP)
	if err != nil {
		socksReply(control, 8)
		return
	}
	bindNetwork := "udp6"
	if local.IP.To4() != nil {
		bindNetwork = "udp4"
	}
	listener, err := net.ListenUDP(bindNetwork, &net.UDPAddr{IP: local.IP, Zone: local.Zone})
	if err != nil {
		socksReply(control, 1)
		return
	}
	if !s.track(listener) {
		return
	}
	bound := listener.LocalAddr().(*net.UDPAddr)
	endpoint := bound.AddrPort()
	endpoint = netip.AddrPortFrom(endpoint.Addr().WithZone("").Unmap(), endpoint.Port())
	s.registerUDPRelay(endpoint, listener)
	defer func() {
		_ = listener.Close()
		s.unregisterUDPRelay(endpoint, listener)
		s.untrack(listener)
	}()
	if !socksUDPBoundReply(control, bound) {
		return
	}
	if err := control.SetDeadline(time.Time{}); err != nil {
		return
	}
	associationCtx, cancel := context.WithCancel(routeinfo.With(ctx, requestInfo(control.RemoteAddr().String(), "socks5")))
	association := &socksUDPAssociation{
		server: s, control: control, listener: listener, ctx: associationCtx,
		sourceIP: sourceIP, idleTimeout: socksUDPIdleTimeout,
		lastActivity: time.Now(), targets: make(map[string]*socksUDPTarget),
	}
	if port != 0 {
		association.client = netip.AddrPortFrom(sourceIP, port)
	}
	association.workers.Add(1)
	go association.receive()
	// The control socket owns the UDP association. A half-close also ends it.
	_, _ = io.Copy(io.Discard, control)
	cancel()
	association.shutdown()
	association.workers.Wait()
}

// parseSOCKSUDPRequest returns the original destination header for the reply:
// it represents the connected target even when a domain resolves to another IP.
func parseSOCKSUDPRequest(packet []byte) (string, []byte, []byte, bool) {
	if len(packet) < 4 || packet[0] != 0 || packet[1] != 0 || packet[2] != 0 {
		return "", nil, nil, false
	}
	var host string
	var end int
	switch packet[3] {
	case 1:
		end = 10
		if len(packet) < end {
			return "", nil, nil, false
		}
		host = net.IP(packet[4:8]).String()
	case 4:
		end = 22
		if len(packet) < end {
			return "", nil, nil, false
		}
		host = net.IP(packet[4:20]).String()
	case 3:
		if len(packet) < 5 || packet[4] == 0 {
			return "", nil, nil, false
		}
		end = 5 + int(packet[4]) + 2
		if len(packet) < end {
			return "", nil, nil, false
		}
		host = string(packet[5 : end-2])
	default:
		return "", nil, nil, false
	}
	port := binary.BigEndian.Uint16(packet[end-2 : end])
	if port == 0 {
		return "", nil, nil, false
	}
	address := net.JoinHostPort(host, strconv.Itoa(int(port)))
	if !validAddress(address) {
		return "", nil, nil, false
	}
	return address, packet[:end], packet[end:], true
}

// registerUDPRelay guards a live relay port: otherwise a datagram addressed to
// another association (or itself) could recursively enter a SOCKS UDP listener.
func (s *Server) registerUDPRelay(endpoint netip.AddrPort, listener *net.UDPConn) {
	s.updateEndpoints(func(e *localEndpoints) { e.udp[endpoint] = listener })
}

// unregisterUDPRelay keeps the entry if the kernel already gave the closed
// listener's port to a newer association.
func (s *Server) unregisterUDPRelay(endpoint netip.AddrPort, listener *net.UDPConn) {
	s.updateEndpoints(func(e *localEndpoints) {
		if e.udp[endpoint] == listener {
			delete(e.udp, endpoint)
		}
	})
}

// ownsUDPAddress matches relay sockets exactly: each binds a specific IP.
func (s *Server) ownsUDPAddress(address string) bool {
	target, err := netip.ParseAddrPort(address)
	if err != nil {
		return false // A hostname is checked against the connected peer after dial.
	}
	return s.endpoints.Load().udp[netip.AddrPortFrom(target.Addr().WithZone("").Unmap(), target.Port())] != nil
}

func (a *socksUDPAssociation) receive() {
	defer a.workers.Done()
	packet := make([]byte, socksUDPMaxPacket)
	for {
		a.mu.Lock()
		deadline := a.lastActivity.Add(a.idleTimeout)
		a.mu.Unlock()
		if err := a.listener.SetReadDeadline(deadline); err != nil {
			return
		}
		n, source, err := a.listener.ReadFromUDPAddrPort(packet)
		if err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				a.mu.Lock()
				idle := time.Since(a.lastActivity) >= a.idleTimeout
				a.mu.Unlock()
				if !idle {
					continue
				}
				_ = a.control.Close()
			}
			return
		}
		if source.Addr().WithZone("").Unmap() != a.sourceIP || source.Port() == 0 {
			continue
		}
		address, header, payload, valid := parseSOCKSUDPRequest(packet[:n])
		if !valid {
			continue
		}
		a.forward(source.Port(), address, header, payload)
	}
}

func (a *socksUDPAssociation) forward(port uint16, address string, header, payload []byte) {
	relay := &a.server.udp
	if a.server.localDestination("udp", address) {
		relay.localDestination.Add(1)
		a.server.countManagementRefusal(address)
		return
	}
	a.mu.Lock()
	if a.closed || (a.client.IsValid() && a.client.Port() != port) {
		a.mu.Unlock()
		return
	}
	if !a.client.IsValid() {
		a.client = netip.AddrPortFrom(a.sourceIP, port)
	}
	now := time.Now()
	a.lastActivity = now
	if target := a.targets[address]; target != nil {
		target.lastUsed = now
		a.mu.Unlock()
		a.writeTarget(target, payload)
		return
	}
	// receive() forwards synchronously: without this, every datagram to a
	// refusing destination would stall the association for a full dial.
	if routes := relay.routes.Load(); a.failureRoutes != routes {
		a.failures, a.failureRoutes = nil, routes
	}
	if retry, failed := a.failures[address]; failed {
		if now.Before(retry) {
			a.mu.Unlock()
			relay.recentFailure.Add(1)
			return
		}
		delete(a.failures, address)
	}
	// Make room before dialing: even an in-flight connection counts toward the
	// per-association cap. Removing the map entry also prevents stale responses.
	// The evicted target's global slot passes to the new dial.
	var oldest *socksUDPTarget
	if len(a.targets) == socksUDPMaxTargets {
		for _, candidate := range a.targets {
			if oldest == nil || candidate.lastUsed.Before(oldest.lastUsed) {
				oldest = candidate
			}
		}
		delete(a.targets, oldest.address)
	}
	a.mu.Unlock()
	if oldest != nil {
		a.server.closeTracked(oldest.conn)
	} else if !relay.acquire() {
		return
	}

	conn, err := a.server.dialContext(a.ctx, "udp", address)
	if err != nil {
		relay.release()
		if errors.Is(err, errLocalDestination) || errors.Is(err, errLocalPeer) {
			relay.localDestination.Add(1)
		}
		a.rememberFailure(address) // No direct socket fallback when the routed outbound refuses UDP.
		return
	}
	if !a.server.track(conn) {
		relay.release()
		return
	}
	a.mu.Lock()
	if a.closed || a.ctx.Err() != nil {
		a.mu.Unlock()
		relay.release()
		a.server.closeTracked(conn)
		return
	}
	target := &socksUDPTarget{address: address, header: append([]byte(nil), header...), conn: conn, lastUsed: time.Now()}
	a.targets[address] = target
	a.mu.Unlock()
	if !a.writeTarget(target, payload) {
		return
	}
	a.workers.Add(1)
	go a.readTarget(target)
}

// rememberFailure drops datagrams to address for the retry delay. Entries are
// per association, so one client's failures never affect another client. An
// entry recorded across a configuration apply is discarded by the next forward.
func (a *socksUDPAssociation) rememberFailure(address string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	now := time.Now()
	if a.failures == nil {
		a.failures = make(map[string]time.Time)
	}
	if len(a.failures) >= socksUDPMaxTargets {
		soonest := ""
		for candidate, retry := range a.failures {
			if !now.Before(retry) {
				delete(a.failures, candidate)
			} else if soonest == "" || retry.Before(a.failures[soonest]) {
				soonest = candidate
			}
		}
		if len(a.failures) >= socksUDPMaxTargets {
			delete(a.failures, soonest)
		}
	}
	a.failures[address] = now.Add(a.server.udp.retryDelay)
}

func (a *socksUDPAssociation) writeTarget(target *socksUDPTarget, payload []byte) bool {
	if err := target.conn.SetWriteDeadline(time.Now().Add(socksUDPWriteTimeout)); err != nil {
		a.forget(target)
		return false
	}
	n, err := target.conn.Write(payload)
	if err != nil || n != len(payload) {
		a.forget(target)
		return false
	}
	// SetReadDeadline is safe concurrently with the target reader, and keeps
	// an active request from expiring while it waits for its response.
	if err := target.conn.SetReadDeadline(time.Now().Add(a.idleTimeout)); err != nil {
		a.forget(target)
		return false
	}
	return true
}

func (a *socksUDPAssociation) readTarget(target *socksUDPTarget) {
	defer a.workers.Done()
	defer a.forget(target)
	// Each target owns its packet buffer, including room for the SOCKS header.
	// A full-sized upstream datagram is never silently truncated.
	packet := make([]byte, socksUDPMaxPacket+len(target.header))
	copy(packet, target.header)
	for {
		a.mu.Lock()
		if a.closed || a.targets[target.address] != target {
			a.mu.Unlock()
			return
		}
		deadline := target.lastUsed.Add(a.idleTimeout)
		a.mu.Unlock()
		if err := target.conn.SetReadDeadline(deadline); err != nil {
			return
		}
		n, err := target.conn.Read(packet[len(target.header):])
		if err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				a.mu.Lock()
				active := !a.closed && a.targets[target.address] == target && time.Since(target.lastUsed) < a.idleTimeout
				a.mu.Unlock()
				if active {
					continue
				}
			}
			return
		}
		// A SOCKS reply is itself a UDP datagram; oversized packets must be
		// dropped, never sent as a truncated datagram.
		maxPacket := 65527 // IPv6 maximum UDP payload
		if a.sourceIP.Is4() {
			maxPacket = 65507
		}
		if len(target.header)+n > maxPacket {
			continue
		}
		// Never hold the association lock across a socket write: a slow client
		// would otherwise stall forward() and every other target reader.
		a.mu.Lock()
		client := a.client
		active := !a.closed && a.targets[target.address] == target && client.IsValid()
		a.mu.Unlock()
		if !active {
			continue
		}
		if err := a.listener.SetWriteDeadline(time.Now().Add(socksUDPWriteTimeout)); err != nil {
			continue
		}
		if written, err := a.listener.WriteToUDPAddrPort(packet[:len(target.header)+n], client); err != nil || written != len(target.header)+n {
			continue
		}
		stamp := time.Now()
		a.mu.Lock()
		if a.targets[target.address] == target {
			target.lastUsed = stamp
			a.lastActivity = stamp
		}
		a.mu.Unlock()
	}
}

func (a *socksUDPAssociation) forget(target *socksUDPTarget) {
	a.mu.Lock()
	if a.targets[target.address] != target {
		a.mu.Unlock()
		return
	}
	delete(a.targets, target.address)
	a.mu.Unlock()
	a.server.udp.release()
	a.server.closeTracked(target.conn)
}

func (a *socksUDPAssociation) shutdown() {
	a.mu.Lock()
	a.closed = true
	targets := a.targets
	a.targets = nil
	a.mu.Unlock()
	_ = a.listener.Close()
	for _, target := range targets {
		a.server.udp.release()
		a.server.closeTracked(target.conn)
	}
}
