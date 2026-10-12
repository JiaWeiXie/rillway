package app

import (
	"context"
	"rillway/internal/config"
	"rillway/internal/resourcecontrol"
	"testing"
)

func TestActiveMemoryMinimumProtectsRetiredTailscaleOwners(t *testing.T) {
	r := &Runtime{cfg: config.Config{}, tailscaleOwners: map[string]*managed{"retired": {retired: true, active: 1}}}
	s := resourcecontrol.MemoryStatus{Supported: true, MinimumBytes: 256 * resourcecontrol.MiB, MaximumBytes: 4 * resourcecontrol.GiB}
	if got := r.memoryMinimum(s); got.MinimumBytes != resourcecontrol.GiB {
		t.Fatal(got)
	}
	r.tailscaleOwners["retired"].closed = true
	if got := r.memoryMinimum(s); got.MinimumBytes != 256*resourcecontrol.MiB {
		t.Fatal(got)
	}
	r.cfg.Outbounds = []config.Outbound{{Type: "wireguard", Enabled: true}}
	if got := r.memoryMinimum(s); got.MinimumBytes != resourcecontrol.GiB {
		t.Fatal(got)
	}
	s.MaximumBytes = 512 * resourcecontrol.MiB
	if got := r.memoryMinimum(s); got.Supported {
		t.Fatal("insufficient host capacity accepted")
	}
}

func TestUnavailableMemoryControlCannotReportSuccessfulWrite(t *testing.T) {
	s, err := resourcecontrol.MemoryControl(context.Background(), nil)
	if err != nil || s.Supported {
		t.Skip("installed controller is present; avoid touching a live service")
	}
	r := &Runtime{}
	after, err := r.ApplyMemory(context.Background(), resourcecontrol.MemoryRequest{Mode: "MiB", Value: "512"})
	if err == nil || after.Supported {
		t.Fatal("unavailable platform accepted a memory update")
	}
}
