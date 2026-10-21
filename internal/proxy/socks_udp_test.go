package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"rillway/internal/access"
	"testing"
	"time"
)

func udpSOCKSAssociation(t *testing.T, s *Server, auth bool, requestedPort uint16) (net.Conn, *net.UDPAddr) {
	t.Helper()
	control := startSOCKS(t, s)
	methods := []byte{5, 1, 0}
	choice := [2]byte{5, 0}
	if auth {
		methods = []byte{5, 2, 0, 2}
		choice[1] = 2
	}
	if _, err := control.Write(methods); err != nil {
		t.Fatal(err)
	}
	var response [2]byte
	if _, err := io.ReadFull(control, response[:]); err != nil || response != choice {
		t.Fatalf("SOCKS method = %v, err = %v", response, err)
	}
	if auth {
		credentials := append([]byte{1, 4, 'u', 's', 'e', 'r', 8}, []byte("password")...)
		if _, err := control.Write(credentials); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(control, response[:]); err != nil || response != [2]byte{1, 0} {
			t.Fatalf("SOCKS authentication = %v, err = %v", response, err)
		}
	}
	request := []byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(request[8:], requestedPort)
	if _, err := control.Write(request); err != nil {
		t.Fatal(err)
	}
	var header [4]byte
	if _, err := io.ReadFull(control, header[:]); err != nil || header != [4]byte{5, 0, 0, 1} {
		t.Fatalf("SOCKS UDP reply header = %v, err = %v", header, err)
	}
	var bound [6]byte
	if _, err := io.ReadFull(control, bound[:]); err != nil {
		t.Fatal(err)
	}
	addr := &net.UDPAddr{IP: net.IP(append([]byte(nil), bound[:4]...)), Port: int(binary.BigEndian.Uint16(bound[4:]))}
	if addr.Port == 0 || !addr.IP.Equal(control.LocalAddr().(*net.TCPAddr).IP) {
		t.Fatalf("UDP BND address %v differs from accepted TCP local address %v", addr, control.LocalAddr())
	}
	return control, addr
}

func udpRequestIPv4(ip net.IP, port int, payload []byte) []byte {
	packet := make([]byte, 10+len(payload))
	packet[3] = 1
	copy(packet[4:8], ip.To4())
	binary.BigEndian.PutUint16(packet[8:10], uint16(port))
	copy(packet[10:], payload)
	return packet
}

func udpEcho(t *testing.T) *net.UDPConn {
	t.Helper()
	origin, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = origin.Close() })
	go func() {
		var buffer [65535]byte
		for {
			n, peer, err := origin.ReadFromUDP(buffer[:])
			if err != nil {
				return
			}
			_, _ = origin.WriteToUDP(buffer[:n], peer)
		}
	}()
	return origin
}

func readUDPEcho(t *testing.T, client *net.UDPConn, bound *net.UDPAddr, header, payload []byte) {
	t.Helper()
	if err := client.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var packet [65535]byte
	n, peer, err := client.ReadFromUDP(packet[:])
	if err != nil {
		t.Fatal(err)
	}
	if !peer.IP.Equal(bound.IP) || peer.Port != bound.Port {
		t.Fatalf("UDP response source = %v, want %v", peer, bound)
	}
	want := append(append([]byte(nil), header...), payload...)
	if string(packet[:n]) != string(want) {
		t.Fatalf("UDP response = %q, want %q", packet[:n], want)
	}
}

func readNoUDP(t *testing.T, client *net.UDPConn) {
	t.Helper()
	if err := client.SetReadDeadline(time.Now().Add(120 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var packet [65535]byte
	if n, peer, err := client.ReadFromUDP(packet[:]); err == nil {
		t.Fatalf("unexpected UDP response from %v: %q", peer, packet[:n])
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("UDP read = %v, want timeout", err)
	}
}

func TestSOCKSUDPAssociateEchoAndDatagramBoundaries(t *testing.T) {
	s := testServer(t, false)
	control, bound := udpSOCKSAssociation(t, s, false, 0)
	origin := udpEcho(t)
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: control.LocalAddr().(*net.TCPAddr).IP})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	address := origin.LocalAddr().(*net.UDPAddr)
	for _, payload := range [][]byte{[]byte("first"), {}, []byte("third datagram")} {
		request := udpRequestIPv4(address.IP, address.Port, payload)
		if _, err := client.WriteToUDP(request, bound); err != nil {
			t.Fatal(err)
		}
		readUDPEcho(t, client, bound, request[:10], payload)
	}
}

func TestSOCKSUDPPinsPortOnlyAfterValidPacket(t *testing.T) {
	s := testServer(t, false)
	control, bound := udpSOCKSAssociation(t, s, false, 0)
	origin := udpEcho(t).LocalAddr().(*net.UDPAddr)
	bind := &net.UDPAddr{IP: control.LocalAddr().(*net.TCPAddr).IP}
	attacker, err := net.ListenUDP("udp4", bind)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attacker.Close() }()
	legitimate, err := net.ListenUDP("udp4", bind)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = legitimate.Close() }()
	valid := udpRequestIPv4(origin.IP, origin.Port, []byte("allowed"))
	fragment := append([]byte(nil), valid...)
	fragment[2] = 1
	if _, err := attacker.WriteToUDP(fragment, bound); err != nil {
		t.Fatal(err)
	}
	readNoUDP(t, attacker)
	if _, err := legitimate.WriteToUDP(valid, bound); err != nil {
		t.Fatal(err)
	}
	readUDPEcho(t, legitimate, bound, valid[:10], []byte("allowed"))
	if _, err := attacker.WriteToUDP(valid, bound); err != nil {
		t.Fatal(err)
	}
	readNoUDP(t, attacker)
}

func TestSOCKSUDPExplicitPortRejectsOtherPortsAndAddresses(t *testing.T) {
	s := testServer(t, false)
	origin := udpEcho(t).LocalAddr().(*net.UDPAddr)
	legitimate, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = legitimate.Close() }()
	_, bound := udpSOCKSAssociation(t, s, false, uint16(legitimate.LocalAddr().(*net.UDPAddr).Port))
	otherPort, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = otherPort.Close() }()
	packet := udpRequestIPv4(origin.IP, origin.Port, []byte("authorized"))
	if _, err := otherPort.WriteToUDP(packet, bound); err != nil {
		t.Fatal(err)
	}
	readNoUDP(t, otherPort)

	// Loopback aliases may be unavailable on some hosts; the source-IP check is
	// also exercised against the accepted peer in the parser and association.
	otherIP, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: legitimate.LocalAddr().(*net.UDPAddr).Port})
	if err == nil {
		defer func() { _ = otherIP.Close() }()
		if _, err := otherIP.WriteToUDP(packet, bound); err != nil {
			t.Fatal(err)
		}
		readNoUDP(t, otherIP)
	}
	if _, err := legitimate.WriteToUDP(packet, bound); err != nil {
		t.Fatal(err)
	}
	readUDPEcho(t, legitimate, bound, packet[:10], []byte("authorized"))
}

func TestSOCKSUDPRejectsMalformedHeadersAndMissingPort(t *testing.T) {
	s := testServer(t, false)
	control, bound := udpSOCKSAssociation(t, s, false, 0)
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: control.LocalAddr().(*net.TCPAddr).IP})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	origin := udpEcho(t).LocalAddr().(*net.UDPAddr)
	valid := udpRequestIPv4(origin.IP, origin.Port, []byte("ok"))
	badReserved := append([]byte(nil), valid...)
	badReserved[0] = 1
	badPort := append([]byte(nil), valid...)
	badPort[8], badPort[9] = 0, 0
	badDomain := []byte{0, 0, 0, 3, 3, 'a', 'b', 'c', 0}
	badIPv6 := []byte{0, 0, 0, 4, 1, 2, 3, 4}
	for _, packet := range [][]byte{badReserved, badPort, badDomain, badIPv6, {0, 0, 0, 5}} {
		if _, err := client.WriteToUDP(packet, bound); err != nil {
			t.Fatal(err)
		}
		readNoUDP(t, client)
	}
	if _, err := client.WriteToUDP(valid, bound); err != nil {
		t.Fatal(err)
	}
	readUDPEcho(t, client, bound, valid[:10], []byte("ok"))
}

func TestSOCKSUDPDomainTargetAndIPv6Header(t *testing.T) {
	t.Run("domain", func(t *testing.T) {
		s := testServer(t, false)
		control, bound := udpSOCKSAssociation(t, s, false, 0)
		origin := udpEcho(t).LocalAddr().(*net.UDPAddr)
		client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: control.LocalAddr().(*net.TCPAddr).IP})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = client.Close() }()
		name := "127.0.0.1"
		header := append([]byte{0, 0, 0, 3, byte(len(name))}, name...)
		header = append(header, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(origin.Port))
		payload := []byte("domain")
		if _, err := client.WriteToUDP(append(append([]byte(nil), header...), payload...), bound); err != nil {
			t.Fatal(err)
		}
		readUDPEcho(t, client, bound, header, payload)
	})
	t.Run("IPv6", func(t *testing.T) {
		origin, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
		if err != nil {
			t.Skipf("IPv6 loopback unavailable: %v", err)
		}
		defer func() { _ = origin.Close() }()
		go func() {
			var packet [65535]byte
			for {
				n, addr, err := origin.ReadFromUDP(packet[:])
				if err != nil {
					return
				}
				_, _ = origin.WriteToUDP(packet[:n], addr)
			}
		}()
		s := testServer(t, false)
		front, err := net.Listen("tcp6", "[::1]:0")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- s.ServeSOCKS(ctx, front) }()
		defer func() {
			cancel()
			_ = s.Close()
			<-done
		}()
		control, err := net.Dial("tcp6", front.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = control.Close() }()
		_ = control.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := control.Write([]byte{5, 1, 0}); err != nil {
			t.Fatal(err)
		}
		var choice [2]byte
		if _, err := io.ReadFull(control, choice[:]); err != nil || choice != [2]byte{5, 0} {
			t.Fatalf("IPv6 SOCKS method = %v, err = %v", choice, err)
		}
		if _, err := control.Write([]byte{5, 3, 0, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
			t.Fatal(err)
		}
		var reply [22]byte
		if _, err := io.ReadFull(control, reply[:]); err != nil || reply[1] != 0 || reply[3] != 4 {
			t.Fatalf("IPv6 UDP BND reply = %v, err = %v", reply, err)
		}
		bound := &net.UDPAddr{IP: net.IP(append([]byte(nil), reply[4:20]...)), Port: int(binary.BigEndian.Uint16(reply[20:]))}
		client, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = client.Close() }()
		packet := make([]byte, 22)
		packet[3] = 4
		copy(packet[4:20], net.IPv6loopback)
		binary.BigEndian.PutUint16(packet[20:], uint16(origin.LocalAddr().(*net.UDPAddr).Port))
		packet = append(packet, []byte("ipv6")...)
		if _, err := client.WriteToUDP(packet, bound); err != nil {
			t.Fatal(err)
		}
		readUDPEcho(t, client, bound, packet[:22], []byte("ipv6"))
	})
}

func TestSOCKSUDPCloseAndSourceDisconnectReleaseSocket(t *testing.T) {
	for _, reason := range []string{"control", "source", "server"} {
		t.Run(reason, func(t *testing.T) {
			s := testServer(t, false)
			control, bound := udpSOCKSAssociation(t, s, false, 0)
			origin := udpEcho(t).LocalAddr().(*net.UDPAddr)
			client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: control.LocalAddr().(*net.TCPAddr).IP})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			request := udpRequestIPv4(origin.IP, origin.Port, []byte("before close"))
			if _, err := client.WriteToUDP(request, bound); err != nil {
				t.Fatal(err)
			}
			readUDPEcho(t, client, bound, request[:10], []byte("before close"))
			switch reason {
			case "control":
				if err := control.Close(); err != nil {
					t.Fatal(err)
				}
			case "source":
				if n := s.DisconnectSource(netip.MustParseAddr("127.0.0.1")); n != 1 {
					t.Fatalf("disconnected %d SOCKS controls", n)
				}
			case "server":
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(time.Second)
			for {
				l, err := net.ListenUDP("udp4", bound)
				if err == nil {
					_ = l.Close()
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("UDP socket remained bound after %s teardown: %v", reason, err)
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}

func TestSOCKSUDPAuthenticatedAndGuarded(t *testing.T) {
	t.Run("authenticated", func(t *testing.T) {
		s := testServer(t, true)
		control, bound := udpSOCKSAssociation(t, s, true, 0)
		origin := udpEcho(t).LocalAddr().(*net.UDPAddr)
		client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: control.LocalAddr().(*net.TCPAddr).IP})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = client.Close() }()
		packet := udpRequestIPv4(origin.IP, origin.Port, []byte("authenticated"))
		if _, err := client.WriteToUDP(packet, bound); err != nil {
			t.Fatal(err)
		}
		readUDPEcho(t, client, bound, packet[:10], []byte("authenticated"))
	})
	t.Run("denied source", func(t *testing.T) {
		s := testServer(t, false)
		policy, err := access.Compile([]access.RuleSpec{{ID: "deny-loopback", Action: "deny", CIDRs: []string{"127.0.0.1/32"}, Enabled: true}})
		if err != nil {
			t.Fatal(err)
		}
		s.SetSourcePolicy(policy)
		client := startSOCKS(t, s)
		// The admission guard may close the socket before this write completes.
		_, _ = client.Write([]byte{5, 1, 0, 5, 3, 0, 1, 0, 0, 0, 0, 0, 0})
		var reply [1]byte
		if n, err := client.Read(reply[:]); err == nil || n != 0 {
			t.Fatalf("denied SOCKS source received %d bytes: %v", n, err)
		}
	})
}

// Invalid requests must be refused without binding a UDP port or dialing out.
func TestSOCKSUDPRejectsMismatchedAssociateSource(t *testing.T) {
	s := testServer(t, false)
	control := startSOCKS(t, s)
	if _, err := control.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	var choice [2]byte
	if _, err := io.ReadFull(control, choice[:]); err != nil || choice != [2]byte{5, 0} {
		t.Fatalf("SOCKS method = %v, err = %v", choice, err)
	}
	if _, err := control.Write([]byte{5, 3, 0, 1, 127, 0, 0, 2, 0, 0}); err != nil {
		t.Fatal(err)
	}
	var reply [10]byte
	if _, err := io.ReadFull(control, reply[:]); err != nil || reply[1] == 0 {
		t.Fatalf("mismatched source accepted: %v, err = %v", reply, err)
	}
}

type udpCaptureDialer struct {
	connections chan net.Conn
}

func (d udpCaptureDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err == nil {
		d.connections <- conn
	}
	return conn, err
}

func TestSOCKSUDPBoundsAndEvictsTargetSockets(t *testing.T) {
	const count = socksUDPMaxTargets + 1
	origins := make([]*net.UDPConn, 0, count)
	for range count {
		origin, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		origins = append(origins, origin)
		defer func() { _ = origin.Close() }()
	}
	dialer := udpCaptureDialer{connections: make(chan net.Conn, count)}
	s := testServer(t, false)
	s.dialer = dialer
	control, bound := udpSOCKSAssociation(t, s, false, 0)
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: control.LocalAddr().(*net.TCPAddr).IP})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	var first net.Conn
	for i, origin := range origins {
		addr := origin.LocalAddr().(*net.UDPAddr)
		packet := udpRequestIPv4(addr.IP, addr.Port, []byte("request"))
		if _, err := client.WriteToUDP(packet, bound); err != nil {
			t.Fatal(err)
		}
		select {
		case conn := <-dialer.connections:
			if i == 0 {
				first = conn
			}
		case <-time.After(2 * time.Second):
			t.Fatal("UDP target dial did not complete")
		}
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := first.Write([]byte("evicted")); errors.Is(err, net.ErrClosed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("least recently used destination remained open")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.mu.Lock()
	total := len(s.connections)
	s.mu.Unlock()
	if total > socksUDPMaxTargets+2 { // TCP control + UDP relay listener + destinations
		t.Fatalf("tracked sockets = %d, want at most %d", total, socksUDPMaxTargets+2)
	}
}

func TestSOCKSUDPIdleTargetExpires(t *testing.T) {
	s := testServer(t, false)
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	origin, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = origin.Close() }()
	conn, err := net.Dial("udp4", origin.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	if !s.track(conn) {
		t.Fatal("server closed before target registration")
	}
	address := origin.LocalAddr().String()
	target := &socksUDPTarget{address: address, conn: conn, header: []byte{0, 0, 0, 1, 127, 0, 0, 1, 0, 1}, lastUsed: time.Now()}
	a := &socksUDPAssociation{server: s, listener: listener, idleTimeout: 25 * time.Millisecond, targets: map[string]*socksUDPTarget{address: target}}
	a.workers.Add(1)
	go a.readTarget(target)
	done := make(chan struct{})
	go func() { a.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		a.shutdown()
		t.Fatal("idle UDP target did not expire")
	}
	a.mu.Lock()
	remaining := len(a.targets)
	a.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d idle targets remained", remaining)
	}
}

func TestSOCKSUDPIdleAssociationClosesControl(t *testing.T) {
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	control, client := net.Pipe()
	defer func() { _ = control.Close(); _ = client.Close() }()
	a := &socksUDPAssociation{
		control: control, listener: listener, sourceIP: netip.MustParseAddr("127.0.0.1"),
		idleTimeout: 25 * time.Millisecond, lastActivity: time.Now(),
	}
	a.workers.Add(1)
	go a.receive()
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var packet [1]byte
	if _, err := client.Read(packet[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("idle UDP association did not close control: %v", err)
	}
	a.workers.Wait()
}

// The outbound is allowed to reject UDP. The association must not bypass it.
func TestSOCKSUDPNoDirectFallback(t *testing.T) {
	s, calls := metadataServer(t)
	control, bound := udpSOCKSAssociation(t, s, false, 0)
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: control.LocalAddr().(*net.TCPAddr).IP})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	name := "route.example"
	packet := append([]byte{0, 0, 0, 3, byte(len(name))}, name...)
	packet = append(packet, 1, 187, 'n', 'o')
	if _, err := client.WriteToUDP(packet, bound); err != nil {
		t.Fatal(err)
	}
	select {
	case call := <-calls:
		if call.network != "udp" || call.address != "route.example:443" || call.info.Inbound != "socks5" || call.info.SourceIP != netip.MustParseAddr("127.0.0.1") {
			t.Fatalf("routed datagram = %+v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("UDP dial did not reach the configured outbound")
	}
	readNoUDP(t, client)
}

func TestSOCKSUDPRejectsOwnRelayDestination(t *testing.T) {
	s, calls := metadataServer(t)
	control, bound := udpSOCKSAssociation(t, s, false, 0)
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: control.LocalAddr().(*net.TCPAddr).IP})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	request := udpRequestIPv4(bound.IP, bound.Port, []byte("self"))
	if _, err := client.WriteToUDP(request, bound); err != nil {
		t.Fatal(err)
	}
	readNoUDP(t, client)
	select {
	case call := <-calls:
		t.Fatalf("routed datagram back into SOCKS UDP listener: %+v", call)
	default:
	}
}

func TestParseSOCKSUDPRequestMalformed(t *testing.T) {
	valid := []byte{0, 0, 0, 3, 1, 'a', 0, 53, 'x'}
	address, header, payload, ok := parseSOCKSUDPRequest(valid)
	if !ok || address != "a:53" || len(header) != 8 || string(payload) != "x" {
		t.Fatalf("parsed datagram = %q %v %q %v", address, header, payload, ok)
	}
	for _, packet := range [][]byte{{}, {0, 0, 1, 1, 127, 0, 0, 1, 0, 53}, {0, 0, 0, 3, 0, 0, 53}, {0, 0, 0, 1, 127, 0, 0, 1, 0, 0}, {0, 0, 0, 4, 0}} {
		if _, _, _, ok := parseSOCKSUDPRequest(packet); ok {
			t.Fatalf("accepted malformed UDP request %v", packet)
		}
	}
}

// Check that relaying replies never confuses one destination with another.
func TestSOCKSUDPMultipleDestinations(t *testing.T) {
	s := testServer(t, false)
	control, bound := udpSOCKSAssociation(t, s, false, 0)
	first, second := udpEcho(t), udpEcho(t)
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: control.LocalAddr().(*net.TCPAddr).IP})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	for i, origin := range []*net.UDPConn{first, second, first} {
		peer := origin.LocalAddr().(*net.UDPAddr)
		message := []byte{byte('0' + i)}
		request := udpRequestIPv4(peer.IP, peer.Port, message)
		if _, err := client.WriteToUDP(request, bound); err != nil {
			t.Fatal(err)
		}
		readUDPEcho(t, client, bound, request[:10], message)
	}
}
