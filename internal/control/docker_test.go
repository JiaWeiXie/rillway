package control

import (
	"encoding/json"
	"net/http/httptest"
	"rillway/internal/config"
	"rillway/internal/dockerproxy"
	"strings"
	"testing"
)

func TestDockerExportAPIAuthenticationValidationAndNoMutation(t *testing.T) {
	b := newBackend()
	b.cfg = config.Default("private-state")
	b.cfg.PAC.ProxyAddress = "192.168.1.10:17890"
	b.cfg.Security.ProxyUsername = "private-user"
	b.cfg.Security.ProxyPasswordFile = "private-file"
	h := New(b, "correct")
	for _, tc := range []struct {
		method, body, token, origin, language string
		code                                  int
	}{
		{"GET", "", "", "", "en", 401},
		{"GET", "", "correct", "", "en", 200},
		{"POST", `{"proxy_url":"http://proxy:80","no_proxy":"corp.example"}`, "correct", "http://other.example", "en", 403},
		{"POST", `{"proxy_url":"http://proxy:80","no_proxy":"corp.example"}`, "correct", "", "en", 200},
		{"POST", `{"proxy_url":"http://user@proxy:80","no_proxy":""}`, "correct", "", "zh-Hant", 422},
		{"POST", `{"proxy_url":"http://proxy:80","no_proxy":"","license":"secret"}`, "correct", "", "en", 400},
	} {
		r := httptest.NewRequest(tc.method, "/api/v1/integrations/docker", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Accept-Language", tc.language)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code || strings.Contains(w.Body.String(), "secret@") || strings.Contains(w.Body.String(), "private-") {
			t.Fatalf("response %d: %s", w.Code, w.Body.String())
		}
		if tc.code == 200 {
			var bundle dockerproxy.Bundle
			if err := json.Unmarshal(w.Body.Bytes(), &bundle); err != nil || len(bundle.Exports) != 4 || !bundle.AuthRequired {
				t.Fatal(bundle, err)
			}
		} else if tc.language == "zh-Hant" && !strings.Contains(w.Body.String(), "Docker Proxy 網址") {
			t.Fatal(w.Body.String())
		}
	}
	if b.cfg.Revision != 1 || b.cfg.PAC.ProxyAddress != "192.168.1.10:17890" || b.lastAction != "" {
		t.Fatal("export changed runtime configuration")
	}
}
