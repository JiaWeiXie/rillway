package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"rillway/internal/config"
	"rillway/internal/control"
	"rillway/internal/i18n"
	"rillway/internal/outbound"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOutboundRefreshIntervalCycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, err := control.NewClient(server.URL, "token", "")
	if err != nil {
		t.Fatal(err)
	}

	m := model{
		ctx:               context.Background(),
		client:            client,
		ready:             true,
		page:              1,
		width:             100,
		height:            30,
		cfg:               config.Config{Outbounds: []config.Outbound{{ID: "direct", Type: "direct", Enabled: true}}},
		outboundRefresh:   30 * time.Second,
		refreshConfigured: true,
		nextStatus:        time.Now().Add(30 * time.Second),
	}

	// Initial default view
	view := m.View()
	if !strings.Contains(view, "Auto refresh: 30s") {
		t.Fatalf("expected view to contain 'Auto refresh: 30s', got:\n%s", view)
	}

	// Cycle 1: 30s -> 1m (60s)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = next.(model)
	if m.outboundRefresh != time.Minute {
		t.Fatalf("expected 1m (60s), got %v", m.outboundRefresh)
	}
	if !strings.Contains(m.View(), "Auto refresh: 1m") {
		t.Fatalf("expected view to contain 'Auto refresh: 1m', got:\n%s", m.View())
	}

	// Cycle 2: 1m -> 5m (300s)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = next.(model)
	if m.outboundRefresh != 5*time.Minute {
		t.Fatalf("expected 5m (300s), got %v", m.outboundRefresh)
	}
	if !strings.Contains(m.View(), "Auto refresh: 5m") {
		t.Fatalf("expected view to contain 'Auto refresh: 5m', got:\n%s", m.View())
	}

	// Cycle 3: 5m -> Off (0s)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = next.(model)
	if m.outboundRefresh != 0 {
		t.Fatalf("expected Off (0), got %v", m.outboundRefresh)
	}
	if !strings.Contains(m.View(), "Auto refresh: Off") {
		t.Fatalf("expected view to contain 'Auto refresh: Off', got:\n%s", m.View())
	}

	// Cycle 4: Off -> 10s
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = next.(model)
	if m.outboundRefresh != 10*time.Second {
		t.Fatalf("expected 10s, got %v", m.outboundRefresh)
	}
	if !strings.Contains(m.View(), "Auto refresh: 10s") {
		t.Fatalf("expected view to contain 'Auto refresh: 10s', got:\n%s", m.View())
	}

	// Cycle 5: 10s -> 30s
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = next.(model)
	if m.outboundRefresh != 30*time.Second {
		t.Fatalf("expected 30s, got %v", m.outboundRefresh)
	}
	if !strings.Contains(m.View(), "Auto refresh: 30s") {
		t.Fatalf("expected view to contain 'Auto refresh: 30s', got:\n%s", m.View())
	}

	// Bilingual view check: Traditional Chinese
	m.locale = i18n.TraditionalChinese
	zhView := m.View()
	if !strings.Contains(zhView, "自動更新：30 秒") {
		t.Fatalf("expected zh-Hant view to contain '自動更新：30 秒', got:\n%s", zhView)
	}

	// Switch to Off in Traditional Chinese
	m.outboundRefresh = 0
	if !strings.Contains(m.View(), "自動更新：關閉") {
		t.Fatalf("expected zh-Hant view to contain '自動更新：關閉', got:\n%s", m.View())
	}
}

func TestOutboundRefreshTickSafety(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, err := control.NewClient(server.URL, "token", "")
	if err != nil {
		t.Fatal(err)
	}

	base := model{
		ctx:               context.Background(),
		client:            client,
		ready:             true,
		page:              1,
		width:             100,
		height:            30,
		cfg:               config.Config{Outbounds: []config.Outbound{{ID: "direct", Type: "direct", Enabled: true}}},
		outboundRefresh:   30 * time.Second,
		refreshConfigured: true,
		nextStatus:        time.Now().Add(-time.Second), // expired deadline
	}

	// Polling must stay inside the Outbounds page, avoid active requests/forms,
	// and never advance the deadline when it did not dispatch status loading.
	for _, page := range []int{0, 2, 3} {
		m := base
		m.page = page
		next, _ := m.Update(tickMsg(time.Now()))
		if next.(model).statusLoading {
			t.Fatalf("page %d scheduled status load during tick", page)
		}
	}

	for _, test := range []struct {
		name  string
		apply func(*model)
	}{
		{"open form", func(m *model) { m.form = "outbound" }},
		{"loading", func(m *model) { m.loading = true }},
		{"status loading", func(m *model) { m.statusLoading = true }},
		{"off", func(m *model) { m.outboundRefresh = 0 }},
		{"unexpired deadline", func(m *model) { m.nextStatus = time.Now().Add(25 * time.Second) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := base
			test.apply(&m)
			before := m.nextStatus
			next, _ := m.Update(tickMsg(time.Now()))
			result := next.(model)
			if test.name == "status loading" {
				if !result.statusLoading {
					t.Fatal("statusLoading state was cleared")
				}
			} else if result.statusLoading {
				t.Fatal("scheduled status load during protected tick")
			}
			if !result.nextStatus.Equal(before) {
				t.Fatal("protected tick advanced outbound refresh deadline")
			}
		})
	}

	// A valid tick dispatches a status load and advances its deadline.
	{
		m := base
		next, cmd := m.Update(tickMsg(time.Now()))
		result := next.(model)
		if !result.statusLoading {
			t.Fatal("expected statusLoading=true on valid tick")
		}
		if cmd == nil {
			t.Fatal("expected tick command batch to be scheduled")
		}
		if !result.nextStatus.After(time.Now()) {
			t.Fatal("nextStatus was not updated to future deadline")
		}
	}
}

func TestOutboundForcedRefreshViaR(t *testing.T) {
	var queryReceived atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/outbounds") {
			queryReceived.Store(r.URL.RawQuery)
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, err := control.NewClient(server.URL, "token", "")
	if err != nil {
		t.Fatal(err)
	}

	m := model{
		ctx:               context.Background(),
		client:            client,
		ready:             true,
		page:              1,
		width:             100,
		height:            30,
		cfg:               config.Config{Outbounds: []config.Outbound{{ID: "direct", Type: "direct", Enabled: true}}},
		outboundRefresh:   30 * time.Second,
		refreshConfigured: true,
		nextStatus:        time.Now().Add(10 * time.Second),
	}

	// Pressing r on Outbounds page dispatches loadStatuses(true)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = next.(model)
	if cmd == nil || !m.statusLoading {
		t.Fatal("r on page 1 did not set statusLoading=true or schedule command")
	}

	// Normal loadStatuses(false) on page switch or background
	cmdNormal := m.loadStatuses(false, m.statusSequence)
	msgNormal := cmdNormal().(statusesMsg)
	_ = msgNormal
	if q, ok := queryReceived.Load().(string); ok && strings.Contains(q, "refresh=true") {
		t.Fatalf("loadStatuses(false) sent refresh=true: %s", q)
	}

	// Forced loadStatuses(true) explicitly requests ?refresh=true
	cmdForce := m.loadStatuses(true, m.statusSequence)
	msgForce := cmdForce().(statusesMsg)
	_ = msgForce
	if q, ok := queryReceived.Load().(string); !ok || !strings.Contains(q, "refresh=true") {
		t.Fatalf("loadStatuses(true) did not send refresh=true, got: %v", q)
	}
}

func TestStatusesIgnoreOutOfOrderResponse(t *testing.T) {
	m := model{generation: 4, statusSequence: 2, statusLoading: true, statuses: []outbound.Status{{ID: "new"}}}

	next, _ := m.Update(statusesMsg{generation: 4, sequence: 1, statuses: []outbound.Status{{ID: "stale"}}})
	result := next.(model)
	if !result.statusLoading || len(result.statuses) != 1 || result.statuses[0].ID != "new" {
		t.Fatalf("stale status response changed current state: %+v", result.statuses)
	}

	next, _ = result.Update(statusesMsg{generation: 4, sequence: 2, statuses: []outbound.Status{{ID: "fresh"}}})
	result = next.(model)
	if result.statusLoading || len(result.statuses) != 1 || result.statuses[0].ID != "fresh" {
		t.Fatalf("fresh status response was not applied: %+v", result.statuses)
	}
}
