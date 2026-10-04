package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/control"
	"rillway/internal/i18n"
	"rillway/internal/outbound"
	"rillway/internal/platform"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfigAPIProtectsBuiltInDirectAndPersistsReferenceReplacement(t *testing.T) {
	c := config.Default(t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	r, err := New(t.Context(), path, c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	h := control.New(r, "token")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*config.Config){
		func(c *config.Config) { c.Outbounds = c.Outbounds[1:] },
		func(c *config.Config) { c.Outbounds[0].ID = "renamed" },
		func(c *config.Config) { c.Outbounds[0].Enabled = false },
		func(c *config.Config) { c.Outbounds[0].Type = "warp"; c.Outbounds[0].ProxyAddress = "127.0.0.1:40000" },
	} {
		next := r.Config()
		change(&next)
		body, _ := json.Marshal(next)
		req := httptest.NewRequest("PUT", "/api/v1/config", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept-Language", "zh-Hant")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		after, readErr := os.ReadFile(path)
		if w.Code != 422 || !strings.Contains(w.Body.String(), "內建 direct") || readErr != nil || string(before) != string(after) || r.Config().Revision != 1 {
			t.Fatal(w.Code, w.Body.String(), readErr)
		}
	}
	req := httptest.NewRequest("DELETE", "/api/v1/outbounds/warp", strings.NewReader(`{"revision":1,"replacement":"direct"}`))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	disk, err := config.Load(path)
	if err != nil || disk.Revision != 2 || disk.Rules[1].Outbound != "direct" || len(disk.Outbounds) != 1 || r.Config().Revision != disk.Revision {
		t.Fatal(disk, err)
	}
}

func TestAtomicApplyAndConflict(t *testing.T) {
	c := config.Default(t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	r, err := New(t.Context(), path, c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	next := r.Config()
	next.Rules = nil
	if err = r.Apply(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	if r.Config().Revision != 2 || len(r.Config().Rules) != 0 {
		t.Fatal("config was not atomically applied")
	}
	if err = r.Apply(t.Context(), next); !errors.Is(err, config.ErrConflict) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	next = r.Config()
	next.Outbounds = append(next.Outbounds, config.Outbound{ID: "broken", Type: "wireguard", Enabled: true, ConfigFile: "/nonexistent/rillway.conf"})
	if err = r.Apply(t.Context(), next); err == nil {
		t.Fatal("broken outbound accepted")
	}
	if r.Config().Revision != 2 {
		t.Fatal("failed update changed revision")
	}
	disk, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Revision != 2 || len(disk.Outbounds) != 2 {
		t.Fatal("failed update changed disk")
	}
	next = r.Config()
	next.Listeners.Admin = "127.0.0.1:20000"
	if err = r.Apply(t.Context(), next); err == nil {
		t.Fatal("listener change needs restart")
	}
}

type testProvider struct {
	closed  atomic.Int32
	actions atomic.Int32
	peer    net.Conn
}

func (p *testProvider) Action(context.Context, string, string) error { p.actions.Add(1); return nil }

func (p *testProvider) ID() string { return "test" }
func (p *testProvider) DialContext(context.Context, string, string) (net.Conn, error) {
	a, b := net.Pipe()
	p.peer = b
	return a, nil
}

func (p *testProvider) Status(context.Context) outbound.Status { return outbound.Status{ID: p.ID()} }
func (p *testProvider) Close() error                           { p.closed.Add(1); return nil }

func TestProviderRetirementKeepsActiveStream(t *testing.T) {
	p := &testProvider{}
	m := &managed{Provider: p}
	conn, err := m.DialContext(t.Context(), "tcp", "example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.peer.Close() }()
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	if p.closed.Load() != 0 {
		t.Fatal("provider closed an active stream")
	}
	if err = m.Action(t.Context(), "connect", ""); err == nil || p.actions.Load() != 0 {
		t.Fatal("retired provider accepted an action")
	}
	if _, err = m.DialContext(t.Context(), "tcp", "example.com:443"); err == nil {
		t.Fatal("retired provider accepted a new stream")
	}
	go func() { _, _ = p.peer.Write([]byte("still alive")) }()
	b := make([]byte, 11)
	if _, err = io.ReadFull(conn, b); err != nil {
		t.Fatal(err)
	}
	if string(b) != "still alive" {
		t.Fatal("stream corrupted")
	}
	_ = conn.Close()
	_ = conn.Close()
	if p.closed.Load() != 1 {
		t.Fatal("provider did not close exactly once")
	}
}

func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func TestDaemonTLSProxyPACAndShutdown(t *testing.T) {
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
	ctx, cancel := context.WithCancel(i18n.WithLocale(t.Context(), i18n.TraditionalChinese))
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, path, c, func(message string) { ready <- message }) }()
	select {
	case message := <-ready:
		if !strings.Contains(message, "管理權杖檔案："+c.Security.AdminTokenFile) || strings.Contains(message, token) {
			t.Fatal("startup message must localize the label, preserve the path, and hide the token")
		}
	case err := <-done:
		t.Fatal(err)
	case <-time.After(15 * time.Second):
		t.Fatal("daemon not ready")
	}
	api, err := control.NewClient("https://"+c.Listeners.Admin, token, c.Security.TLSCertFile)
	if err != nil {
		t.Fatal(err)
	}
	active, err := api.Config(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if active.Revision != 1 {
		t.Fatal("wrong active revision")
	}
	bad, _ := control.NewClient("https://"+c.Listeners.Admin, "wrong", c.Security.TLSCertFile)
	if _, err = bad.Config(t.Context()); err == nil {
		t.Fatal("unauthorized API accepted")
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("credentials leaked")
		}
		_, _ = io.WriteString(w, "rillway works")
	}))
	defer origin.Close()
	proxyURL, _ := url.Parse("http://" + c.Listeners.HTTP)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	response, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(body) != "rillway works" {
		t.Fatalf("proxy response: %s %v", body, err)
	}
	direct := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 3 * time.Second}
	defer direct.CloseIdleConnections()
	response, err = direct.Get("http://" + c.Listeners.PAC + "/proxy.pac")
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || !strings.Contains(string(body), "FindProxyForURL") {
		t.Fatal("PAC not served")
	}
	stats, err := api.Snapshot(t.Context())
	if err != nil || !strings.Contains(string(stats), "config_revision") {
		t.Fatalf("stats unavailable: %s %v", stats, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("daemon did not shut down")
	}
}

func TestSaveFailureDoesNotApply(t *testing.T) {
	c := config.Default(t.TempDir())
	r, err := New(t.Context(), t.TempDir(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	next := r.Config()
	next.Rules = nil
	if err = r.Apply(t.Context(), next); err == nil {
		t.Fatal("saving to directory succeeded")
	}
	if r.Config().Revision != 1 || len(r.Config().Rules) != 2 {
		t.Fatal("failed disk save changed active config")
	}
}
