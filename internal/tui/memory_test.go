package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"rillway/internal/control"
	"rillway/internal/i18n"
	"rillway/internal/memorylimit"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMemoryEditorUsesAPIAndPreservesRevision(t *testing.T) {
	s := memorylimit.Status{Supported: true, HostBytes: 4 * memorylimit.GiB, MinimumBytes: 256 * memorylimit.MiB, MaximumBytes: memorylimit.Maximum(4 * memorylimit.GiB), LimitBytes: 512 * memorylimit.MiB, Mode: "MiB", Value: "512", Revision: "old", MinimumReason: "Direct and WARP safety floor"}
	var captured memorylimit.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/service/memory" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("wrong API or authentication")
		}
		if r.Method == "PUT" {
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Error(err)
			}
		}
		_ = json.NewEncoder(w).Encode(s)
	}))
	defer server.Close()
	client, err := control.NewClient(server.URL, "secret", "")
	if err != nil {
		t.Fatal(err)
	}
	m := model{ctx: context.Background(), client: client, ready: true, page: 3, width: 100, height: 40}
	modelAfter, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m = modelAfter.(model)
	if cmd == nil || !m.memoryLoading {
		t.Fatal("m did not fetch service limits")
	}
	modelAfter, _ = m.Update(cmd())
	m = modelAfter.(model)
	if m.form != "memory" || m.fields[1] != "512" || m.memoryStatus.Revision != "old" {
		t.Fatal(m.form, m.fields)
	}
	other := s
	other.Revision = "new"
	modelAfter, _ = m.Update(memoryMsg{status: other})
	m = modelAfter.(model)
	if m.memoryStatus.Revision != "old" {
		t.Fatal("background response replaced editor revision")
	}
	// Left chooses percent, right returns to MiB while preserving the value.
	m.fields[0], m.fields[1] = "GiB", "0.5"
	m.field = 1
	modelAfter, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = modelAfter.(model)
	if cmd == nil || !m.saving {
		t.Fatal("valid memory form not submitted")
	}
	modelAfter, _ = m.Update(cmd())
	m = modelAfter.(model)
	if captured.Mode != "GiB" || captured.Value != "0.5" || captured.Revision != "old" || m.form != "" {
		t.Fatal(captured, m.form)
	}
	m.form = "memory"
	m.fields = []string{"MiB", "1"}
	m.saving = false
	modelAfter, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = modelAfter.(model)
	if cmd != nil || m.err == nil {
		t.Fatal("unsafe minimum submitted")
	}
	m.locale = i18n.TraditionalChinese
	if !strings.Contains(m.View(), "記憶體") {
		t.Fatal(m.View())
	}
}

func TestMemoryUnitConversionKeepsCommonValues(t *testing.T) {
	s := memorylimit.Status{HostBytes: 4 * memorylimit.GiB}
	if mode, value := nextMemoryUnit("MiB", "512", s, false); mode != "GiB" || value != "0.500" {
		t.Fatal(mode, value)
	}
	if mode, value := nextMemoryUnit("GiB", "0.5", s, false); mode != "percent" || value != "12.500" {
		t.Fatal(mode, value)
	}
	if mode, value := nextMemoryUnit("percent", "12.5", s, false); mode != "MiB" || value != "512.000" {
		t.Fatal(mode, value)
	}
	s.HostBytes = 5*memorylimit.GiB + 128*memorylimit.MiB
	s.MinimumBytes = memorylimit.GiB
	s.MaximumBytes = memorylimit.Maximum(s.HostBytes)
	mode, value := nextMemoryUnit("GiB", "1", s, false)
	if _, err := memorylimit.Calculate(memorylimit.Request{Mode: mode, Value: value}, s); err != nil {
		t.Fatal("conversion crossed the minimum", mode, value, err)
	}
}
