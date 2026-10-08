package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/platform"
	"testing"
	"time"
)

func TestControlListenersBoundIncompleteBodies(t *testing.T) {
	c := config.Default(t.TempDir())
	c.Listeners = config.Listeners{HTTP: freeAddress(t), SOCKS5: freeAddress(t), Admin: freeAddress(t), PAC: freeAddress(t)}
	c.PAC.ProxyAddress = c.Listeners.HTTP
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	token, _, err := platform.EnsureCredentials(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- serveWithTimeouts(ctx, path, c, func(string) { close(ready) }, serverTimeouts{200 * time.Millisecond, time.Second, 100 * time.Millisecond, time.Second})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not start")
	}
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	}()

	type result struct {
		name               string
		elapsed            time.Duration
		setupErr, closeErr error
	}
	results := make(chan result, 2)
	probe := func(name string, dial func() (net.Conn, error), request string) {
		started := time.Now()
		conn, err := dial()
		if err != nil {
			results <- result{name: name, elapsed: time.Since(started), setupErr: err}
			return
		}
		defer func() { _ = conn.Close() }()
		if err = conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			results <- result{name: name, elapsed: time.Since(started), setupErr: err}
			return
		}
		if _, err = io.WriteString(conn, request); err != nil {
			results <- result{name: name, elapsed: time.Since(started), setupErr: err}
			return
		}
		_, closeErr := io.Copy(io.Discard, conn)
		results <- result{name: name, elapsed: time.Since(started), closeErr: closeErr}
	}
	go probe("PAC", func() (net.Conn, error) {
		return net.DialTimeout("tcp", c.Listeners.PAC, time.Second)
	}, "GET /proxy.pac HTTP/1.1\r\nHost: localhost\r\nContent-Length: 100\r\n\r\n")
	go probe("Admin", func() (net.Conn, error) {
		return tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", c.Listeners.Admin, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // isolated loopback test with generated self-signed certificate
	}, fmt.Sprintf("PUT /api/v1/config HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n", token))
	for range 2 {
		got := <-results
		if got.setupErr != nil {
			t.Fatalf("%s request setup failed: %v", got.name, got.setupErr)
		}
		if timeout, ok := got.closeErr.(net.Error); ok && timeout.Timeout() {
			t.Errorf("%s remained open until the client deadline: %v", got.name, got.closeErr)
		}
		if got.elapsed > time.Second {
			t.Errorf("%s incomplete body lasted %v, want <= %v", got.name, got.elapsed, time.Second)
		}
	}
}
