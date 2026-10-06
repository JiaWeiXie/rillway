package control

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestBrowserMemoryFormContract(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, "testdata/memory_test.js").CombinedOutput()
	if err != nil {
		t.Fatalf("memory form: %v\n%s", err, out)
	}
}
