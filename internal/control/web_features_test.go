package control

import (
	"bytes"
	"context"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestBrowserThemeContract(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("Node is required for the browser theme contract: %v", err)
	}
	response := request(New(newBackend(), "token"), "GET", "/theme.js", "", "", "")
	if response.Code != http.StatusOK || response.Body.Len() == 0 {
		t.Fatalf("theme asset unavailable: HTTP %d", response.Code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, node, "testdata/theme_test.js").CombinedOutput()
	if err != nil {
		t.Fatalf("browser theme contract: %v\n%s", err, output)
	}
}

func TestBrowserEditorsContract(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("Node is required for the browser editor contract: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, node, "testdata/editors_test.js").CombinedOutput()
	if err != nil {
		t.Fatalf("browser editor contract: %v\n%s", err, output)
	}
}

func TestBrowserInteractionsContract(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("Node is required for the browser interaction contract: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, node, "testdata/interactions_test.js").CombinedOutput()
	if err != nil {
		t.Fatalf("browser interaction contract: %v\n%s", err, output)
	}
}

func TestEditorsAssetServingContract(t *testing.T) {
	h := New(newBackend(), "private-management-token")
	embedded, err := assets.ReadFile("web/editors.js")
	if err != nil {
		t.Fatal(err)
	}
	w := request(h, "GET", "/editors.js", "", "", "")
	if w.Code != http.StatusOK || len(embedded) == 0 || !bytes.Equal(w.Body.Bytes(), embedded) {
		t.Fatalf("editor route did not serve its embedded script: HTTP %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "javascript") || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("editor asset lost MIME/cache/security headers: %v", w.Header())
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") || strings.Contains(w.Body.String(), "private-management-token") {
		t.Fatal("editor asset changed CSP or exposed the management token")
	}
	head := request(h, "HEAD", "/editors.js", "", "", "")
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("invalid editor HEAD response: HTTP %d", head.Code)
	}
	for _, path := range []string{"/missing.js", "/editors.js/source", "/web/editors.js", "/server.go", "/testdata/editors_test.js"} {
		if w := request(h, "GET", path, "", "", ""); w.Code != http.StatusNotFound {
			t.Fatalf("unexpected static path exposed: %s = %d", path, w.Code)
		}
	}
	if w := request(h, "POST", "/editors.js", "", "", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatal("editor asset accepted a mutation method")
	}
}
