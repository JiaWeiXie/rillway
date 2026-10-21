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
	"time"
)

const (
	socksUDPMaxTargets   = 32
	socksUDPIdleTimeout  = 2 * time.Minute
	socksUDPWriteTimeout = 5 * time.Second
	socksUDPMaxPacket    = 65535
)

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

	mu           sync.Mutex
	lastActivity time.Time
	targets      map[string]*socksUDPTarget
	closed       bool
	workers      sync.WaitGroup
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
	s.mu.Lock()
	s.udpEndpoints[endpoint] = struct{}{}
	s.mu.Unlock()
	defer func() {
		_ = listener.Close()
		s.mu.Lock()
		delete(s.udpEndpoints, endpoint)
		s.mu.Unlock()
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

// Also guard live UDP relay ports: otherwise a datagram addressed to another
// association (or itself) could recursively enter a SOCKS UDP listener.
func (s *Server) ownsUDPAddress(address string) bool {
	target, err := netip.ParseAddrPort(address)
	if err != nil {
		return false // A hostname is checked against the connected peer after dial.
	}
	ip := target.Addr().WithZone("").Unmap()
	s.mu.Lock()
	defer s.mu.Unlock()
	for endpoint := range s.udpEndpoints {
		if s.matchesEndpoint(endpoint, target, ip) {
			return true
		}
	}
	return false
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
	if a.server.ownsUDPAddress(address) {
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
	// Make room before dialing: even an in-flight connection counts toward the
	// per-association cap. Removing the map entry also prevents stale responses.
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
		_ = oldest.conn.Close()
		a.server.untrack(oldest.conn)
	}

	conn, err := a.server.dialContext(a.ctx, "udp", address)
	if err != nil {
		return // No direct socket fallback when the routed outbound refuses UDP.
	}
	if a.server.ownsUDPAddress(conn.RemoteAddr().String()) {
		_ = conn.Close()
		return
	}
	if !a.server.track(conn) {
		return
	}
	a.mu.Lock()
	if a.closed || a.ctx.Err() != nil {
		a.mu.Unlock()
		_ = conn.Close()
		a.server.untrack(conn)
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
		a.mu.Lock()
		if !a.closed && a.targets[target.address] == target && a.client.IsValid() {
			if err := a.listener.SetWriteDeadline(time.Now().Add(socksUDPWriteTimeout)); err == nil {
				if written, err := a.listener.WriteToUDPAddrPort(packet[:len(target.header)+n], a.client); err == nil && written == len(target.header)+n {
					stamp := time.Now()
					target.lastUsed = stamp
					a.lastActivity = stamp
				}
			}
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
	_ = target.conn.Close()
	a.server.untrack(target.conn)
}

func (a *socksUDPAssociation) shutdown() {
	a.mu.Lock()
	a.closed = true
	targets := a.targets
	a.targets = nil
	a.mu.Unlock()
	_ = a.listener.Close()
	for _, target := range targets {
		_ = target.conn.Close()
		a.server.untrack(target.conn)
	}
}
