package control

import (
	"context"
	"net/http"
	"os/exec"
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
