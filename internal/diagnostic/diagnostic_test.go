package diagnostic

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"rillway/internal/outbound"
	"strings"
	"testing"
)

type localProvider struct{}

func (localProvider) ID() string                             { return "test" }
func (localProvider) Close() error                           { return nil }
func (localProvider) Status(context.Context) outbound.Status { return outbound.Status{} }
func (localProvider) DialContext(ctx context.Context, n, a string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, n, a)
}

func TestDiagnosticBoundedAndNoRedirect(t *testing.T) {
	var method, rangeHeader string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		rangeHeader = r.Header.Get("Range")
		w.Header().Set("X-Served-By", "test-cache")
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		_, _ = io.Copy(w, strings.NewReader(strings.Repeat("x", 5<<20)))
	}))
	defer s.Close()
	r := Check(t.Context(), localProvider{}, "auto", s.URL, false)
	if r.Error != "" || method != "HEAD" || r.Bytes != 0 || r.CDN["X-Served-By"] != "test-cache" {
		t.Fatalf("HEAD result: %+v", r)
	}
	r = Check(t.Context(), localProvider{}, "ipv4", s.URL, true)
	if r.Error != "" || method != "GET" || r.Bytes != 4<<20 || rangeHeader != "bytes=0-4194303" || r.BytesPerSecond <= 0 {
		t.Fatalf("bounded download: %+v", r)
	}
	r = Check(t.Context(), localProvider{}, "auto", s.URL+"/redirect", true)
	if r.Status != 302 || r.Bytes != 0 {
		t.Fatalf("redirect was followed: %+v", r)
	}
}

func TestDiagnosticVerifiesTLS(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer s.Close()
	r := Check(t.Context(), localProvider{}, "auto", s.URL, false)
	if r.Error == "" {
		t.Fatal("untrusted TLS was accepted")
	}
}
