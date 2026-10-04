package control

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/outbound"
	"strings"
	"sync"
	"testing"
)

type fakeBackend struct {
	mu                     sync.Mutex
	cfg                    config.Config
	failApply, errorAction error
	lastAction, lastValue  string
}

func newBackend() *fakeBackend {
	return &fakeBackend{cfg: config.Config{Version: 1, Revision: 7, Outbounds: []config.Outbound{{ID: "warp", Type: "warp", Enabled: true}}}}
}
func (b *fakeBackend) Config() config.Config { b.mu.Lock(); defer b.mu.Unlock(); return b.cfg }
func (b *fakeBackend) Apply(_ context.Context, c config.Config) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failApply != nil {
		return b.failApply
	}
	if c.Revision != b.cfg.Revision {
		return config.ErrConflict
	}
	c.Revision++
	b.cfg = c
	return nil
}

func (b *fakeBackend) Snapshot() any {
	return map[string]any{"flows": []map[string]any{{"host": "internal.example", "upload_bytes_per_second": 128}}}
}

func (b *fakeBackend) Statuses(context.Context) []outbound.Status {
	return []outbound.Status{{ID: "warp", State: "ready"}}
}

func (b *fakeBackend) Action(_ context.Context, id, action, value string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastAction = id + "/" + action
	b.lastValue = value
	return b.errorAction
}

func request(h http.Handler, method, path, body, token, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://rillway.test"+path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSensitiveEndpointsRequireBearer(t *testing.T) {
	h := New(newBackend(), "correct")
	for _, path := range []string{"/api/v1/config", "/api/v1/stats", "/api/v1/outbounds"} {
		for _, token := range []string{"", "wrong"} {
			w := request(h, "GET", path, "", token, "")
			if w.Code != 401 {
				t.Fatalf("%s = %d", path, w.Code)
			}
			if strings.Contains(w.Body.String(), "internal.example") {
				t.Fatal("unauthenticated statistics leaked")
			}
		}
		if w := request(h, "GET", path, "", "correct", ""); w.Code != 200 {
			t.Fatalf("authenticated %s = %d", path, w.Code)
		}
	}
	if w := request(New(newBackend(), ""), "GET", "/api/v1/config", "", "", ""); w.Code != 503 {
		t.Fatalf("empty admin token = %d", w.Code)
	}
}

func TestConfigCASAndStrictBodies(t *testing.T) {
	b := newBackend()
	h := New(b, "token")
	for _, tc := range []struct {
		name, body string
		status     int
	}{{"invalid", "{", 400}, {"unknown", `{"revision":7,"secret":"bad"}`, 400}, {"trailing", `{"revision":7} {}`, 400}, {"stale", `{"revision":6}`, 409}, {"large", strings.Repeat(" ", 257<<10) + `{}`, 413}} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(h, "PUT", "/api/v1/config", tc.body, "token", "")
			if w.Code != tc.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
	body, _ := json.Marshal(b.Config())
	w := request(h, "PUT", "/api/v1/config", string(body), "token", "http://rillway.test")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if b.Config().Revision != 8 {
		t.Fatal("revision did not advance")
	}
	w = request(h, "PUT", "/api/v1/config", string(body), "token", "")
	if w.Code != 409 {
		t.Fatalf("lost-update protection = %d", w.Code)
	}
}

func TestCrossOriginWriteRejected(t *testing.T) {
	h := New(newBackend(), "token")
	for _, origin := range []string{"http://evil.example", "null", "https://rillway.test", "http://rillway.test/path", "http://rillway.test?query"} {
		w := request(h, "POST", "/api/v1/outbounds/warp/connect", `{}`, "token", origin)
		if w.Code != 403 {
			t.Fatalf("origin %q accepted: %d", origin, w.Code)
		}
	}
	for _, origin := range []string{"", "http://rillway.test"} {
		w := request(h, "POST", "/api/v1/outbounds/warp/connect", `{}`, "token", origin)
		if w.Code != 200 {
			t.Fatalf("valid origin %q: %d", origin, w.Code)
		}
	}
}

func TestActionDoesNotEchoCredentials(t *testing.T) {
	b := newBackend()
	secret := "my-secret-license"
	b.errorAction = errors.New("invalid credential " + secret)
	h := New(b, "token")
	w := request(h, "POST", "/api/v1/outbounds/warp/license", `{"value":"`+secret+`"}`, "token", "")
	if w.Code != 422 || b.lastValue != secret {
		t.Fatalf("wrong action result: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("license echoed in error")
	}
	for _, tc := range []struct {
		path, body string
		status     int
	}{{"warp/unknown", `{}`, 400}, {"missing/connect", `{}`, 404}, {"warp/license", `{"value":""}`, 400}, {"warp/license", `{"value":"` + strings.Repeat("x", 5000) + `"}`, 413}} {
		w := request(h, "POST", "/api/v1/outbounds/"+tc.path, tc.body, "token", "")
		if w.Code != tc.status {
			t.Fatalf("%s = %d", tc.path, w.Code)
		}
	}
}

func TestConcurrentConfigEditsOnlyOneWins(t *testing.T) {
	b := newBackend()
	server := httptest.NewServer(New(b, "token"))
	defer server.Close()
	client, err := NewClient(server.URL, "token", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := b.Config()
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := client.Apply(context.Background(), cfg); results <- err }()
	}
	wins, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		var apiErr *APIError
		if err == nil {
			wins++
		} else if errors.As(err, &apiErr) && apiErr.Status == 409 {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
}

func TestEmbeddedUIAndSecurityHeaders(t *testing.T) {
	h := New(newBackend(), "hidden-admin-secret")
	for _, path := range []string{"/", "/app.js", "/app.css"} {
		w := request(h, "GET", path, "", "", "")
		if w.Code != 200 {
			t.Fatalf("%s = %d", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "hidden-admin-secret") {
			t.Fatal("secret embedded in UI")
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatal("missing CSP")
		}
	}
	if w := request(h, "GET", "/server.go", "", "", ""); w.Code != 404 {
		t.Fatal("unexpected asset exposed")
	}
	w := request(h, "GET", "/", "", "", "")
	for _, text := range []string{"lang=\"zh-Hant\"", "id=\"flows\"", "id=\"license-value\" type=\"password\"", "/app.js"} {
		if !strings.Contains(w.Body.String(), text) {
			t.Fatalf("missing UI contract %s", text)
		}
	}
}

func TestClientTLSVerificationAndCustomCA(t *testing.T) {
	server := httptest.NewTLSServer(New(newBackend(), "token"))
	defer server.Close()
	untrusted, err := NewClient(server.URL, "token", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = untrusted.Config(context.Background()); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	certPath := filepath.Join(t.TempDir(), "ca.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err = os.WriteFile(certPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	trusted, err := NewClient(server.URL, "token", certPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = trusted.Config(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = NewClient("http://192.168.1.2:9443", "token", ""); err == nil {
		t.Fatal("remote cleartext token allowed")
	}
	if _, err = NewClient("https://token@example.com", "token", ""); err == nil {
		t.Fatal("userinfo allowed in management URL")
	}
}

func TestWrongContentTypeAndBackendFailure(t *testing.T) {
	b := newBackend()
	h := New(b, "token")
	r := httptest.NewRequest("PUT", "http://rillway.test/api/v1/config", bytes.NewBufferString(`{}`))
	r.Header.Set("Authorization", "Bearer token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatal(w.Code)
	}
	b.failApply = errors.New("sensitive parser details")
	w = request(h, "PUT", "/api/v1/config", `{"revision":7}`, "token", "")
	body, _ := io.ReadAll(w.Result().Body)
	if w.Code != 422 || strings.Contains(string(body), "sensitive") {
		t.Fatal("backend error leaked")
	}
	b.failApply = config.ErrConflict
	w = request(h, "PUT", "/api/v1/config", `{"revision":7}`, "token", "")
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
}

func TestExplicitPublicErrorsReachAPIWithoutPrivateCause(t *testing.T) {
	const secret = "private-license-and-command-output"
	for _, tc := range []struct {
		name, method, path, body, message string
	}{
		{"apply", "PUT", "/api/v1/config", `{"revision":7}`, "監聽位置變更需要重新啟動服務"},
		{"action", "POST", "/api/v1/outbounds/warp/connect", `{}`, "目前 WARP 版本不支援 Proxy 模式，請更新官方用戶端"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBackend()
			cause := errors.New(secret)
			public := config.PublicError{Message: tc.message, Err: cause}
			wrapped := fmt.Errorf("private context %s: %w", secret, public)
			if tc.name == "apply" {
				b.failApply = wrapped
			} else {
				b.errorAction = wrapped
			}
			w := request(New(b, "token"), tc.method, tc.path, tc.body, "token", "")
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != 422 || body.Error != tc.message {
				t.Fatalf("safe message missing: %d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), secret) || strings.Contains(public.Error(), secret) {
				t.Fatal("private cause or wrapper leaked")
			}
			if !errors.Is(public, cause) {
				t.Fatal("underlying cause cannot be classified")
			}
		})
	}
}

func TestEmptyPublicMessageUsesSafeFallback(t *testing.T) {
	err := config.PublicError{Err: errors.New("secret")}
	if message := publicMessage(err, "safe fallback"); message != "safe fallback" {
		t.Fatalf("unexpected message %q", message)
	}
}
