package control

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticationFailureLimit(t *testing.T) {
	h := New(newBackend(), "test-token")
	send := func(token, peer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://rillway.test/api/v1/config", nil)
		r.RemoteAddr = peer
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Forwarded-For", "192.0.2.99")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for range 50 {
		if w := send("test-token", "192.0.2.2:1000"); w.Code != 200 {
			t.Fatal("valid polling throttled", w.Code)
		}
	}
	for range 20 {
		if w := send("invalid", "192.0.2.1:1000"); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	// Changing source ports or providing valid credentials cannot bypass a block.
	w := send("test-token", "192.0.2.1:2000")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "60" {
		t.Fatal(w.Code, w.Header())
	}
	if w := send("test-token", "192.0.2.2:3000"); w.Code != 200 {
		t.Fatal("unrelated peer blocked", w.Code)
	}
}
