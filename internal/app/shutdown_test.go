package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"rillway/internal/config"
	"testing"
	"time"
)

// Ordinary HTTP requests are not hijacked. Their contexts must inherit the
// daemon cancellation so a stalled origin cannot outlive daemon shutdown.
func TestShutdownCancelsSlowHTTPOrigin(t *testing.T) {
	c := config.Default(t.TempDir())
	c.Listeners = config.Listeners{HTTP: freeAddress(t), SOCKS5: freeAddress(t), Admin: freeAddress(t), PAC: freeAddress(t)}
	c.PAC.ProxyAddress = c.Listeners.HTTP
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	started, stopped := make(chan struct{}), make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(stopped) }))
	defer func() { cancel(); origin.Close() }()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, path, c, func(string) { close(ready) }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not start")
	}
	proxyURL, err := url.Parse("http://" + c.Listeners.HTTP)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		response, e := client.Get(origin.URL)
		if e == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("origin request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown left the slow HTTP request running")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("origin request context survived shutdown")
	}
	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("client stream survived shutdown")
	}
}
