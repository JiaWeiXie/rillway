package tui

import (
	"context"
	"os"
	"rillway/internal/resourcecontrol"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestServiceSwitchResetsWarpLogLoading(t *testing.T) {
	tokenFile := t.TempDir() + "/token"
	if err := os.WriteFile(tokenFile, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := model{
		ctx:            context.Background(),
		form:           "connection",
		fields:         []string{"https://example.com", tokenFile, ""},
		warpLogLoading: true,
		warpLogStatus:  resourcecontrol.WarpLogStatus{Supported: true},
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.warpLogLoading || m.warpLogStatus.Supported {
		t.Fatalf("service switch retained WARP log state: %+v", m.warpLogStatus)
	}
}
