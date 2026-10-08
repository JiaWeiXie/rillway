package app

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"rillway/internal/config"
	"strings"
	"testing"
)

func TestPACRejectsWebPageLoads(t *testing.T) {
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
	for _, tt := range []struct {
		name, site, mode, dest string
		want                   int
	}{
		{"system", "", "", "", http.StatusOK},
		{"user", "none", "", "", http.StatusOK},
		{"script", "cross-site", "no-cors", "script", http.StatusForbidden},
		{"fetch", "same-origin", "cors", "empty", http.StatusForbidden},
		{"iframe", "same-origin", "navigate", "iframe", http.StatusForbidden},
		{"navigation", "cross-site", "navigate", "document", http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/proxy.pac", nil)
			req.Header.Set("Sec-Fetch-Site", tt.site)
			req.Header.Set("Sec-Fetch-Mode", tt.mode)
			req.Header.Set("Sec-Fetch-Dest", tt.dest)
			w := httptest.NewRecorder()
			pacHandler(r).ServeHTTP(w, req)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d", w.Code, tt.want)
			}
			if tt.want == http.StatusOK {
				if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "FindProxyForURL") {
					t.Fatalf("invalid PAC response: headers=%v body=%s", w.Header(), w.Body.String())
				}
			}
		})
	}
}
