package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/engine"
	"rillway/internal/platform"
	"strings"
	"testing"
	"time"
)

// The management and PAC listeners answer their own clients only. A proxy client's
// CONNECT or forward request for them is refused: otherwise the daemon would
// connect from the host's own address and the source ACL would stop being a boundary.
func TestManagementAndPACAreNotReachableThroughTheProxy(t *testing.T) {
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
	cert, err := os.ReadFile(c.Security.TLSCertFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(cert) {
		t.Fatal("invalid generated certificate")
	}
	proxyURL, _ := url.Parse("http://" + c.Listeners.HTTP)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	directTransport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	defer directTransport.CloseIdleConnections()
	direct := &http.Client{Transport: directTransport, Timeout: 3 * time.Second}
	ctx, cancel := context.WithCancel(t.Context())
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- Serve(ctx, path, c, func(string) { close(ready) }) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("service did not stop")
		}
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("service did not start")
	}
	request := func(c *http.Client, address, auth string, want int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		r, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Body.Close() }()
		body, err := io.ReadAll(r.Body)
		if err != nil || r.StatusCode != want {
			t.Fatal("response", r.StatusCode, "expected", want, err)
		}
		return body
	}
	stats := func() engine.Snapshot {
		t.Helper()
		var snap engine.Snapshot
		if err := json.Unmarshal(request(direct, "https://"+c.Listeners.Admin+"/api/v1/stats", token, http.StatusOK), &snap); err != nil {
			t.Fatal(err)
		}
		return snap
	}
	// Direct clients keep working.
	request(direct, "https://"+c.Listeners.Admin+"/api/v1/config", "", http.StatusUnauthorized)
	if body := request(direct, "http://"+c.Listeners.PAC+"/proxy.pac", "", http.StatusOK); !strings.Contains(string(body), "FindProxyForURL") {
		t.Fatal("PAC unavailable")
	}
	// Through the proxy, by literal address or by a name the selected outbound
	// resolves to the same listener, every management, PAC and proxy endpoint is refused,
	// with or without credentials.
	aliasPAC := strings.Replace(c.Listeners.PAC, "127.0.0.1", "localhost", 1)
	aliasAdmin := strings.Replace(c.Listeners.Admin, "127.0.0.1", "localhost", 1)
	aliasHTTP := strings.Replace(c.Listeners.HTTP, "127.0.0.1", "localhost", 1)
	for _, target := range []struct{ url, auth string }{
		{"https://" + c.Listeners.Admin + "/api/v1/info", ""},
		{"https://" + c.Listeners.Admin + "/api/v1/stats", token},
		{"https://" + aliasAdmin + "/api/v1/info", ""},
		{"http://" + c.Listeners.PAC + "/proxy.pac", ""},
		{"http://" + aliasPAC + "/proxy.pac", ""},
		{"http://" + c.Listeners.HTTP + "/", ""},
		{"http://" + aliasHTTP + "/", ""},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if target.auth != "" {
			req.Header.Set("Authorization", "Bearer "+target.auth)
		}
		r, err := client.Do(req)
		switch {
		case err != nil: // CONNECT refused: the client reports the proxy's 502.
			if !strings.Contains(err.Error(), "Bad Gateway") {
				t.Fatal(target.url, "failed for another reason:", err)
			}
		case r.StatusCode != http.StatusBadGateway:
			_ = r.Body.Close()
			t.Fatal(target.url, "was reachable through the proxy:", r.StatusCode)
		default:
			_ = r.Body.Close()
		}
	}
	if snap := stats(); len(snap.Flows) != 0 || len(snap.Destinations) != 0 || snap.Totals != (engine.Totals{}) {
		t.Fatal("refused management, PAC or proxy-loop traffic was observed", snap)
	}
	// The diagnostics count the refused management and PAC attempts: the five
	// requests above (admin info, stats, the localhost alias of admin, PAC literal
	// and PAC alias), not the two for the proxy's own listener.
	var diagnostics struct {
		ManagementRefusals uint64 `json:"proxy_management_refusals"`
	}
	if err := json.Unmarshal(request(direct, "https://"+c.Listeners.Admin+"/api/v1/stats", token, http.StatusOK), &diagnostics); err != nil {
		t.Fatal(err)
	}
	if diagnostics.ManagementRefusals != 5 {
		t.Fatal("proxy_management_refusals =", diagnostics.ManagementRefusals, "want 5")
	}
	// A separate origin on the very same host is ordinary observable traffic.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ordinary traffic")
	}))
	defer origin.Close()
	request(client, origin.URL, "", http.StatusOK)
	if snap := stats(); len(snap.Flows) != 1 || snap.Totals.DownloadBytes == 0 {
		t.Fatal("unrelated local origin was hidden", snap)
	}
}
