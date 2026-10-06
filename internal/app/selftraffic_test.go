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

func TestOwnManagementAndPACStayOutOfProxyStatistics(t *testing.T) {
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
	request := func(address, auth string, want int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		r, err := client.Do(req)
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
		if err := json.Unmarshal(request("https://"+c.Listeners.Admin+"/api/v1/stats", token, http.StatusOK), &snap); err != nil {
			t.Fatal(err)
		}
		return snap
	}
	// Refreshing one's own statistics through CONNECT must not recursively
	// generate new flows or inflate the counters used by the next refresh.
	for range 3 {
		if snap := stats(); len(snap.Flows) != 0 || len(snap.Destinations) != 0 || snap.Totals != (engine.Totals{}) {
			t.Fatal("management refresh observed itself", snap)
		}
	}
	request("https://"+c.Listeners.Admin+"/api/v1/config", "", http.StatusUnauthorized)
	if body := request("http://"+c.Listeners.PAC+"/proxy.pac", "", http.StatusOK); !strings.Contains(string(body), "FindProxyForURL") {
		t.Fatal("PAC unavailable")
	}
	// Resolve localhost using the selected outbound rather than extra host DNS.
	aliasPAC := strings.Replace(c.Listeners.PAC, "127.0.0.1", "localhost", 1)
	request("http://"+aliasPAC+"/proxy.pac", "", http.StatusOK)
	aliasHTTP := strings.Replace(c.Listeners.HTTP, "127.0.0.1", "localhost", 1)
	request("http://"+aliasHTTP+"/", "", http.StatusBadGateway)
	if snap := stats(); len(snap.Flows) != 0 || len(snap.Destinations) != 0 || snap.Totals != (engine.Totals{}) {
		t.Fatal("PAC or refused proxy loop was observed", snap)
	}
	// A separate origin on the very same host is ordinary observable traffic.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ordinary traffic")
	}))
	defer origin.Close()
	request(origin.URL, "", http.StatusOK)
	if snap := stats(); len(snap.Flows) != 1 || snap.Totals.DownloadBytes == 0 {
		t.Fatal("unrelated local origin was hidden", snap)
	}
}
