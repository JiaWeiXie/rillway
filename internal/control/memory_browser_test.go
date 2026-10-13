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
		t.Fatalf("Node is required for the browser form contracts: %v", err)
	}
	for _, script := range []string{"testdata/memory_test.js", "testdata/capacity_test.js"} {
		t.Run(script, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, node, script).CombinedOutput()
			if err != nil {
				t.Fatalf("browser form: %v\n%s", err, out)
			}
		})
	}
}
