package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"rillway/internal/config"
	"testing"
)

func TestFormDefaultsAreAuthenticatedAndReadOnly(t *testing.T) {
	b := newBackend()
	original := b.Config()
	server := New(b, "test-token")
	for _, authorized := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/defaults", nil)
		if authorized {
			req.Header.Set("Authorization", "Bearer test-token")
		}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, req)
		if !authorized {
			if response.Code != 401 {
				t.Fatal("defaults exposed without a token")
			}
			continue
		}
		if response.Code != 200 {
			t.Fatal(response.Body.String())
		}
		var defaults config.FormDefaults
		if err := json.Unmarshal(response.Body.Bytes(), &defaults); err != nil {
			t.Fatal(err)
		}
		if defaults.Outbounds["warp"].ProxyAddress == "" || defaults.Outbounds["warp"].WARPBinary == "" {
			t.Fatal("defaults missing actual values")
		}
		if !reflect.DeepEqual(b.Config(), original) {
			t.Fatal("reading defaults changed configuration")
		}
	}
}
