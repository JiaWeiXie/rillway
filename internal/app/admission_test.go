package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/control"
	"rillway/internal/platform"
	"rillway/internal/proxy"
	"testing"
	"time"
)

func TestProxySaturationPreservesManagementAndShutdown(t *testing.T) {
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
	client, err := control.NewClient("https://"+c.Listeners.Admin, token, c.Security.TLSCertFile)
	if err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewServer(http.NotFoundHandler())
	defer origin.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- Serve(ctx, path, c, func(string) { close(ready) })
		close(done)
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("service did not start")
	}
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("saturated service did not shut down")
		}
	}()
	for range 64 {
		conn, err := net.DialTimeout("tcp", c.Listeners.HTTP, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", origin.Listener.Addr(), origin.Listener.Addr())
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatal("could not fill proxy capacity", response, err)
		}
	}
	// SOCKS uses the same source budget, even before a handshake is supplied.
	excess, err := net.DialTimeout("tcp", c.Listeners.SOCKS5, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = excess.Close() }()
	_ = excess.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = excess.Read(make([]byte, 1)); err == nil {
		t.Fatal("SOCKS bypassed the HTTP source budget")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("excess SOCKS connection remained open")
	}
	raw, err := client.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var stats struct {
		Rejections *proxy.AdmissionRejections `json:"proxy_admission_rejections"`
	}
	if err := json.Unmarshal(raw, &stats); err != nil || stats.Rejections == nil || *stats.Rejections != (proxy.AdmissionRejections{SourceLimit: 1}) {
		t.Fatalf("stats proxy_admission_rejections = %+v, %v", stats.Rejections, err)
	}
	if _, err := client.Config(ctx); err != nil {
		t.Fatal("proxy saturation blocked management", err)
	}
	// Do not rely on client cleanup to release the hijacked HTTP connections.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not close active tunnels")
	}
}
