package app

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"rillway/internal/access"
	"rillway/internal/config"
	"rillway/internal/control"
	"rillway/internal/platform"
	"rillway/internal/proxy"
	"strings"
	"testing"
	"time"
)

func TestProxySaturationPreservesManagementAndShutdown(t *testing.T) {
	c := config.Default(t.TempDir())
	c.Listeners = config.Listeners{HTTP: freeAddress(t), SOCKS5: freeAddress(t), Admin: freeAddress(t), PAC: freeAddress(t)}
	c.ConnectionLimits = config.ConnectionLimits{MaxConnections: 4, MaxConnectionsPerSource: 2}
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
	for range 2 {
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
		Capacity   access.ConnectionCapacity  `json:"proxy_capacity"`
		UDPDrops   *proxy.UDPDrops            `json:"proxy_udp_drops"`
	}
	if err := json.Unmarshal(raw, &stats); err != nil || stats.Rejections == nil || *stats.Rejections != (proxy.AdmissionRejections{SourceLimit: 1}) {
		t.Fatalf("stats proxy_admission_rejections = %+v, %v", stats.Rejections, err)
	}
	if stats.UDPDrops == nil || *stats.UDPDrops != (proxy.UDPDrops{}) {
		t.Fatalf("stats proxy_udp_drops = %+v", stats.UDPDrops)
	}
	if stats.Capacity.ActiveConnections != 2 || stats.Capacity.MaxConnections != 4 || stats.Capacity.MaxConnectionsPerSource != 2 || stats.Capacity.SourceLimit != 1 || stats.Capacity.TotalLimit != 0 {
		t.Fatalf("wrong effective proxy capacity: %+v", stats.Capacity)
	}
	sources, err := client.SourceClients(ctx)
	if err != nil || len(sources.Clients) != 1 {
		t.Fatalf("source snapshot: %+v, %v", sources, err)
	}
	row := sources.Clients[0]
	if sources.Capacity != stats.Capacity || row.ActiveConnections != 2 || row.DeniedConnections != 0 || row.CapacityRejections != 1 || row.SourceLimitRejections != 1 || row.TotalLimitRejections != 0 {
		t.Fatalf("capacity rejected sockets hidden by zero policy denials: %+v", sources)
	}
	if _, err := client.Config(ctx); err != nil {
		t.Fatal("proxy saturation blocked management", err)
	}
	candidate, err := client.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidate.ConnectionLimits = config.ConnectionLimits{MaxConnections: 4, MaxConnectionsPerSource: 3}
	updated, err := client.Apply(ctx, candidate)
	if err != nil {
		t.Fatal("could not raise saturated source limit", err)
	}
	extra, err := net.DialTimeout("tcp", c.Listeners.SOCKS5, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = extra.Close() }()
	_ = extra.SetDeadline(time.Now().Add(time.Second))
	_, _ = extra.Write([]byte{5, 1, 0})
	var greeting [2]byte
	if _, err := io.ReadFull(extra, greeting[:]); err != nil || greeting != [2]byte{5, 0} {
		t.Fatal("raising source limit did not admit SOCKS immediately", greeting, err)
	}
	updated.ConnectionLimits = config.ConnectionLimits{MaxConnections: 1, MaxConnectionsPerSource: 1}
	if _, err := client.Apply(ctx, updated); err != nil {
		t.Fatal("could not lower active limits", err)
	}
	sources, err = client.SourceClients(ctx)
	if err != nil || sources.Capacity.ActiveConnections != 3 || sources.Capacity.MaxConnections != 1 || sources.Capacity.MaxConnectionsPerSource != 1 || sources.Capacity.SourceLimit != 1 {
		t.Fatalf("lowering limit closed old sockets or reset counters: %+v, %v", sources, err)
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

// Idle Web UI keep-alive sockets fill the management budget; macOS must still
// fetch PAC from the same source.
func TestManagementSaturationDoesNotBlockPAC(t *testing.T) {
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
			t.Error("service did not shut down")
		}
	}()
	dialAdmin := func() (*tls.Conn, error) {
		return tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", c.Listeners.Admin, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // isolated loopback test with generated self-signed certificate
	}
	for i := range managementMaxConnectionsPerSource {
		conn, err := dialAdmin()
		if err != nil {
			t.Fatalf("admin connection %d rejected: %v", i+1, err)
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err = fmt.Fprintf(conn, "GET /api/v1/info HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer %s\r\n\r\n", token); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("admin connection %d unusable: %v %v", i+1, response, err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		// The socket now sits idle under the server keep-alive timeout.
	}
	if excess, err := dialAdmin(); err == nil {
		_ = excess.Close()
		t.Fatal("admin per-source budget was not enforced")
	}
	pacClient := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 3 * time.Second}
	response, err := pacClient.Get("http://" + c.Listeners.PAC + "/proxy.pac")
	if err != nil {
		t.Fatal("saturated management budget blocked PAC:", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), "FindProxyForURL") {
		t.Fatalf("PAC response = %d %q %v", response.StatusCode, body, err)
	}
}
