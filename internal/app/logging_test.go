package app

import (
	"context"
	"rillway/internal/config"
	"rillway/internal/resourcecontrol"
	"testing"
)

func TestRuntimeWarpLogStatusAndApply(t *testing.T) {
	c := config.Default(t.TempDir())
	r, err := New(t.Context(), t.TempDir(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	status, err := r.WarpLogStatus(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// On non-Linux (e.g. Darwin test environment), resource control reports supported=false with clear reason
	if status.Supported {
		t.Fatalf("expected unsupported on dev/test environment, got %+v", status)
	}

	applyStatus, err := r.ApplyWarpLog(context.Background(), resourcecontrol.WarpLogRequest{
		Level:    resourcecontrol.WarpLogLevelInfo,
		Revision: "rev-1",
	})
	if err != nil {
		t.Fatalf("unexpected error on apply: %v", err)
	}
	if applyStatus.Supported {
		t.Fatalf("expected unsupported on dev/test environment, got %+v", applyStatus)
	}
}
