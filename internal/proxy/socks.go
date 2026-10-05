package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

func (s *Server) ServeSOCKS(ctx context.Context, listener net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = listener.Close()
		return net.ErrClosed
	}
	s.listeners[listener] = struct{}{}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.listeners, listener); s.mu.Unlock() }()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-done:
		}
	}()
	var workers sync.WaitGroup
	defer workers.Wait()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if !s.allowedClient(conn.RemoteAddr().String()) || !s.track(conn) {
			_ = conn.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { _ = conn.Close() }()
			defer s.untrack(conn)
			s.socksConnection(ctx, conn)
		}()
	}
}

func (s *Server) socksConnection(ctx context.Context, client net.Conn) {
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stop()
	var header [2]byte
	if _, err := io.ReadFull(client, header[:]); err != nil || header[0] != 5 || header[1] == 0 {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(client, methods); err != nil {
		return
	}
	method := byte(0)
	if s.username != "" {
		method = 2
	}
	found := false
	for _, m := range methods {
		if m == method {
			found = true
		}
	}
	if !found {
		_, _ = client.Write([]byte{5, 255})
		return
	}
	if _, err := client.Write([]byte{5, method}); err != nil {
		return
	}
	if method == 2 && !s.socksAuthenticate(client) {
		return
	}
	var request [4]byte
	if _, err := io.ReadFull(client, request[:]); err != nil {
		return
	}
	if request[0] != 5 || request[2] != 0 {
		socksReply(client, 1)
		return
	}
	if request[1] != 1 {
		socksReply(client, 7)
		return
	}
	address, err := readAddress(client, request[3])
	if err != nil {
		socksReply(client, 8)
		return
	}
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	upstream, err := s.dialer.DialContext(dialCtx, "tcp", address)
	cancel()
	if err != nil {
		socksReply(client, 5)
		return
	}
	defer func() { _ = upstream.Close() }()
	if !s.track(upstream) {
		return
	}
	defer s.untrack(upstream)
	if !socksReply(client, 0) {
		return
	}
	_ = client.SetDeadline(time.Time{})
	relay(ctx, client, upstream)
}

func (s *Server) socksAuthenticate(conn net.Conn) bool {
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil || header[0] != 1 || header[1] == 0 {
		return false
	}
	user := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, user); err != nil {
		return false
	}
	var length [1]byte
	if _, err := io.ReadFull(conn, length[:]); err != nil || length[0] == 0 {
		return false
	}
	password := make([]byte, int(length[0]))
	if _, err := io.ReadFull(conn, password); err != nil {
		return false
	}
	accepted := s.authFailures.Allow(conn.RemoteAddr().String()) && same(string(user), s.username) && same(string(password), s.password)
	if !accepted {
		s.authFailures.Failure(conn.RemoteAddr().String())
	}
	status := byte(1)
	if accepted {
		status = 0
	}
	_, err := conn.Write([]byte{1, status})
	return err == nil && accepted
}

func socksReply(w io.Writer, status byte) bool {
	_, err := w.Write([]byte{5, status, 0, 1, 0, 0, 0, 0, 0, 0})
	return err == nil
}

func readAddress(r io.Reader, kind byte) (string, error) {
	var host string
	switch kind {
	case 1:
		var ip [4]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			return "", err
		}
		host = net.IP(ip[:]).String()
	case 4:
		var ip [16]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			return "", err
		}
		host = net.IP(ip[:]).String()
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(r, length[:]); err != nil {
			return "", err
		}
		if length[0] == 0 {
			return "", errors.New("empty SOCKS hostname")
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(r, name); err != nil {
			return "", err
		}
		host = string(name)
	default:
		return "", errors.New("unsupported SOCKS address type")
	}
	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return "", err
	}
	address := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:]))))
	if !validAddress(address) {
		return "", errors.New("invalid SOCKS destination")
	}
	return address, nil
}
