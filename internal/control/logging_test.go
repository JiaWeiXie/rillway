package control

import (
	"context"
	"errors"
	"net/http/httptest"
	"rillway/internal/config"
	"rillway/internal/resourcecontrol"
	"strings"
	"testing"
)

type warpLogTestBackend struct {
	*fakeBackend
	state   resourcecontrol.WarpLogStatus
	failure error
	writes  int
}

func (b *warpLogTestBackend) WarpLogStatus(context.Context) (resourcecontrol.WarpLogStatus, error) {
	return b.state, b.failure
}

func (b *warpLogTestBackend) ApplyWarpLog(_ context.Context, r resourcecontrol.WarpLogRequest) (resourcecontrol.WarpLogStatus, error) {
	if b.failure != nil {
		return b.state, b.failure
	}
	if r.Revision != b.state.Revision {
		return b.state, config.PublicError{Message: resourcecontrol.ErrWarpLogConflict.Error(), Err: resourcecontrol.ErrWarpLogConflict}
	}
	b.state.Level = r.Level
	b.state.Revision = "rev-2"
	b.writes++
	return b.state, nil
}

func TestWarpLogAPIAuthValidationAndConflicts(t *testing.T) {
	b := &warpLogTestBackend{
		fakeBackend: newBackend(),
		state: resourcecontrol.WarpLogStatus{
			Supported:      true,
			Level:          resourcecontrol.WarpLogLevelInfo,
			EffectiveLevel: "info",
			Revision:       "rev-1",
		},
	}
	h := New(b, "token")

	// 1. Auth required
	for _, method := range []string{"GET", "PUT"} {
		if w := request(h, method, "/api/v1/service/logging/warp", `{"level":"debug","revision":"rev-1"}`, "", ""); w.Code != 401 {
			t.Fatalf("expected 401 unauthorized, got %d", w.Code)
		}
	}

	// 2. PUT validations
	for _, tc := range []struct {
		name string
		body string
		code int
	}{
		{"unknown field", `{"level":"debug","revision":"rev-1","extra":"bad"}`, 400},
		{"invalid level", `{"level":"trace","revision":"rev-1"}`, 422},
		{"empty level", `{"level":"","revision":"rev-1"}`, 422},
		{"stale revision", `{"level":"debug","revision":"old"}`, 409},
		{"payload too large", `{"level":"` + strings.Repeat("a", 2049) + `","revision":"rev-1"}`, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := request(h, "PUT", "/api/v1/service/logging/warp", tc.body, "token", ""); w.Code != tc.code || b.writes != 0 {
				t.Fatalf("expected %d, got %d: %s", tc.code, w.Code, w.Body.String())
			}
		})
	}

	// 3. Client and successful PUT
	server := httptest.NewServer(h)
	defer server.Close()
	client, err := NewClient(server.URL, "token", "")
	if err != nil {
		t.Fatal(err)
	}

	status, err := client.WarpLog(context.Background())
	if err != nil || !status.Supported || status.Level != resourcecontrol.WarpLogLevelInfo {
		t.Fatalf("client.WarpLog failed: %+v, err: %v", status, err)
	}

	status, err = client.ApplyWarpLog(context.Background(), resourcecontrol.WarpLogRequest{
		Level:    resourcecontrol.WarpLogLevelDebug,
		Revision: "rev-1",
	})
	if err != nil || status.Level != resourcecontrol.WarpLogLevelDebug || b.writes != 1 {
		t.Fatalf("client.ApplyWarpLog failed: %+v, err: %v", status, err)
	}

	// 4. Backend failure masks private details
	b.failure = errors.New("private systemctl details")
	if w := request(h, "GET", "/api/v1/service/logging/warp", "", "token", ""); w.Code != 503 || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("expected 503 with masked error, got %d: %s", w.Code, w.Body.String())
	}

	// 5. Unsupported backend returns a localized upgrade error on PUT and an
	// English source reason on GET for clients to translate at their surface.
	rawBackend := newBackend()
	rawHandler := New(rawBackend, "token")
	if w := request(rawHandler, "GET", "/api/v1/service/logging/warp", "", "token", ""); w.Code != 200 || strings.Contains(w.Body.String(), "\"supported\":true") {
		t.Fatalf("expected 200 supported:false for un-supported backend, got %d: %s", w.Code, w.Body.String())
	}
	const source = "WARP logging control is unavailable. Upgrade the installed service to enable it."
	const translated = "WARP 記錄控制無法使用；請升級已安裝的服務以啟用此功能。"
	r := httptest.NewRequest("PUT", "http://rillway.test/api/v1/service/logging/warp", strings.NewReader("{\"level\":\"info\",\"revision\":\"rev-1\"}"))
	r.Header.Set("Authorization", "Bearer token")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://rillway.test")
	r.Header.Set("Accept-Language", "zh-Hant")
	w := httptest.NewRecorder()
	rawHandler.ServeHTTP(w, r)
	if w.Code != 501 || !strings.Contains(w.Body.String(), "\"error_source\":\""+source+"\"") || !strings.Contains(w.Body.String(), "\"error\":\""+translated+"\"") {
		t.Fatalf("expected localized unsupported response, got %d: %s", w.Code, w.Body.String())
	}
}
