package outbound

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"time"
)

// dialSOCKS keeps domain resolution at the upstream. SOCKS5 has no way to ask
// for an address family when sending a domain; reject that combination rather
// than silently resolving it outside the tunnel.
func dialSOCKS(ctx context.Context, proxy, network, address string) (net.Conn, error) {
	if err := tcpNetwork(network); err != nil {
		return nil, err
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("invalid destination port")
	}
	req := []byte{5, 1, 0}
	destinationIP := ""
	if ip, e := netip.ParseAddr(host); e == nil {
		ip = ip.Unmap()
		if !familyAllows(network, ip) {
			return nil, errors.New("destination does not match requested IP family")
		}
		destinationIP = ip.String()
		if ip.Is4() {
			req = append(req, 1)
			a := ip.As4()
			req = append(req, a[:]...)
		} else {
			req = append(req, 4)
			a := ip.As16()
			req = append(req, a[:]...)
		}
	} else {
		if network != "tcp" {
			return nil, errors.New("WARP SOCKS5 remote DNS cannot force IPv4/IPv6; use automatic family or a literal IP")
		}
		if len(host) == 0 || len(host) > 255 {
			return nil, errors.New("invalid SOCKS5 hostname length")
		}
		req = append(req, 3, byte(len(host)))
		req = append(req, host...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", proxy)
	if err != nil {
		return nil, fmt.Errorf("WARP proxy listener: %w", err)
	}
	good := false
	defer func() {
		if !good {
			_ = conn.Close()
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, err = conn.Write([]byte{5, 1, 0}); err != nil {
		return nil, err
	}
	var greeting [2]byte
	if _, err = io.ReadFull(conn, greeting[:]); err != nil {
		return nil, err
	}
	if greeting != [2]byte{5, 0} {
		return nil, errors.New("upstream SOCKS5 does not accept unauthenticated local connections")
	}
	if _, err = conn.Write(req); err != nil {
		return nil, err
	}
	var head [4]byte
	if _, err = io.ReadFull(conn, head[:]); err != nil {
		return nil, err
	}
	if head[0] != 5 || head[2] != 0 {
		return nil, errors.New("invalid upstream SOCKS5 reply")
	}
	if head[1] != 0 {
		return nil, socksReplyError(head[1])
	}
	n := 0
	switch head[3] {
	case 1:
		n = 4
	case 4:
		n = 16
	case 3:
		var l [1]byte
		if _, err = io.ReadFull(conn, l[:]); err != nil {
			return nil, err
		}
		n = int(l[0])
	default:
		return nil, errors.New("invalid SOCKS5 address type")
	}
	if _, err = io.CopyN(io.Discard, conn, int64(n+2)); err != nil {
		return nil, err
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	good = true
	return &destinationConn{Conn: conn, ip: destinationIP}, nil
}

func socksReplyError(code byte) error {
	message := fmt.Sprintf("upstream SOCKS5 connect rejected (code %d)", code)
	var cause error
	switch code {
	case 3:
		cause = syscall.ENETUNREACH
	case 4:
		cause = syscall.EHOSTUNREACH
	case 5:
		cause = syscall.ECONNREFUSED
	case 6:
		cause = syscall.ETIMEDOUT
	}
	if cause == nil {
		return errors.New(message)
	}
	return fmt.Errorf("%s: %w", message, cause)
}
