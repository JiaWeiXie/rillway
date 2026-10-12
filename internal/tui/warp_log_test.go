package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"rillway/internal/config"
	"rillway/internal/control"
	"rillway/internal/i18n"
	"rillway/internal/resourcecontrol"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestWarpLogKeyOpensFormWhenSupported(t *testing.T) {
	status := resourcecontrol.WarpLogStatus{
		Supported:      true,
		Level:          resourcecontrol.WarpLogLevelInfo,
		EffectiveLevel: "info",
		Revision:       "rev-1",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/service/logging/warp" {
			_ = json.NewEncoder(w).Encode(status)
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
		ctx:    context.Background(),
		client: client,
		ready:  true,
		page:   3, // Service settings
		width:  100,
		height: 40,
	}

	// Pressing 'l' on page 3 requests warp logging status
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = next.(model)
	if cmd == nil || !m.warpLogLoading {
		t.Fatal("expected l on page 3 to set warpLogLoading and return command")
	}

	// Execute command returning warpLogMsg with open=true
	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(model)

	if m.form != "warp_log" {
		t.Fatalf("expected form to be 'warp_log', got %q", m.form)
	}
	if len(m.fields) != 1 || m.fields[0] != "info" {
		t.Fatalf("expected fields to be ['info'], got %v", m.fields)
	}

	// Form view contains restart warning callout
	view := m.View()
	if !strings.Contains(view, "Applying this setting restarts WARP and briefly interrupts WARP-routed connections") {
		t.Fatalf("expected restart warning notice in view, got:\n%s", view)
	}

	// Traditional Chinese bilingual notice check
	m.locale = i18n.TraditionalChinese
	zhView := m.View()
	if !strings.Contains(zhView, "套用此設定將重啟 WARP，並短暫中斷經由 WARP 分流的連線") {
		t.Fatalf("expected zh-Hant restart warning in view, got:\n%s", zhView)
	}
}

func TestWarpLogUnsupportedShowsErrorWithoutOpeningForm(t *testing.T) {
	status := resourcecontrol.WarpLogStatus{
		Supported: false,
		Reason:    "WARP system logging requires an upgraded Linux service. Update the binary and run sudo rillway service restart.",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/service/logging/warp" {
			_ = json.NewEncoder(w).Encode(status)
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
		ctx:    context.Background(),
		client: client,
		ready:  true,
		page:   3,
		width:  100,
		height: 40,
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected command from l")
	}

	msg := cmd()
	next, _ = m.Update(msg)
	m = next.(model)

	// Form must NOT be opened when unsupported
	if m.form == "warp_log" {
		t.Fatal("form opened despite unsupported status")
	}
	if m.err == nil {
		t.Fatal("expected error to be set when unsupported")
	}
	if !strings.Contains(m.err.Error(), "sudo rillway service restart") {
		t.Fatalf("expected unsupported reason in error, got: %v", m.err)
	}
}

func TestWarpLogLevelCycleAndSubmit(t *testing.T) {
	var capturedRequest atomic.Value
	currentStatus := resourcecontrol.WarpLogStatus{
		Supported:      true,
		Level:          resourcecontrol.WarpLogLevelInfo,
		EffectiveLevel: "info",
		Revision:       "rev-initial",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/service/logging/warp" {
			if r.Method == http.MethodPut {
				var req resourcecontrol.WarpLogRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				capturedRequest.Store(req)
				currentStatus.Level = req.Level
				currentStatus.EffectiveLevel = string(req.Level)
				currentStatus.Revision = "rev-updated"
				_ = json.NewEncoder(w).Encode(currentStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(currentStatus)
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
		ctx:           context.Background(),
		client:        client,
		ready:         true,
		page:          3,
		width:         100,
		height:        40,
		form:          "warp_log",
		fields:        []string{"info"},
		warpLogStatus: currentStatus,
	}

	// 1. Right arrow cycles: info -> debug
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(model)
	if m.fields[0] != "debug" {
		t.Fatalf("expected level debug, got %q", m.fields[0])
	}

	// 2. Space key cycles: debug -> inherit
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(model)
	if m.fields[0] != "inherit" {
		t.Fatalf("expected level inherit, got %q", m.fields[0])
	}

	// 3. Left arrow cycles backwards: inherit -> debug
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = next.(model)
	if m.fields[0] != "debug" {
		t.Fatalf("expected level debug, got %q", m.fields[0])
	}

	// 4. Background status message while editing must NOT overwrite user draft in fields[0]
	backgroundStatus := currentStatus
	backgroundStatus.Level = resourcecontrol.WarpLogLevelWarning
	backgroundStatus.Revision = "rev-background"
	next, _ = m.Update(warpLogMsg{status: backgroundStatus})
	m = next.(model)
	if m.fields[0] != "debug" {
		t.Fatalf("background update replaced user draft: got %q, expected 'debug'", m.fields[0])
	}

	// 5. Submit form via Enter
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil || !m.saving {
		t.Fatal("expected saving=true and command on Enter")
	}

	// Execute apply command
	resultMsg := cmd()
	next, _ = m.Update(resultMsg)
	m = next.(model)

	// Form must be closed upon success
	if m.form != "" {
		t.Fatalf("expected form to be closed, got %q", m.form)
	}
	req, ok := capturedRequest.Load().(resourcecontrol.WarpLogRequest)
	if !ok || req.Level != resourcecontrol.WarpLogLevelDebug {
		t.Fatalf("expected submitted level 'debug', got %+v", req)
	}

	// Success notice displayed
	view := m.View()
	if !strings.Contains(view, "WARP log level saved. WARP service was restarted.") {
		t.Fatalf("expected success message in view, got:\n%s", view)
	}

	// Bilingual success notice in Traditional Chinese
	m.locale = i18n.TraditionalChinese
	zhView := m.View()
	if !strings.Contains(zhView, "WARP 日誌層級已儲存；WARP 服務已重啟。") {
		t.Fatalf("expected zh-Hant success message in view, got:\n%s", zhView)
	}
}

func TestWarpLogConflictPreservesDraft(t *testing.T) {
	currentStatus := resourcecontrol.WarpLogStatus{
		Supported:      true,
		Level:          resourcecontrol.WarpLogLevelInfo,
		EffectiveLevel: "info",
		Revision:       "rev-server",
	}

	var putCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/service/logging/warp" {
			if r.Method == http.MethodPut {
				putCount++
				var req resourcecontrol.WarpLogRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req.Revision == "rev-stale" {
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(map[string]string{
						"error": "WARP logging settings changed elsewhere. Refresh settings before saving.",
					})
					return
				}
				// Second attempt with refreshed revision succeeds
				currentStatus.Level = req.Level
				currentStatus.Revision = "rev-saved"
				_ = json.NewEncoder(w).Encode(currentStatus)
				return
			}
			// GET returns updated revision
			currentStatus.Revision = "rev-refreshed"
			_ = json.NewEncoder(w).Encode(currentStatus)
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
		ctx:           context.Background(),
		client:        client,
		ready:         true,
		page:          3,
		width:         100,
		height:        40,
		form:          "warp_log",
		fields:        []string{"debug"},
		warpLogStatus: resourcecontrol.WarpLogStatus{Supported: true, Level: "info", Revision: "rev-stale"},
	}

	// 1. Submit with stale revision
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil {
		t.Fatal("expected submit cmd")
	}

	// Apply returned 409 conflict
	res := cmd()
	next, conflictCmd := m.Update(res)
	m = next.(model)

	// User draft MUST remain preserved in fields[0] and form MUST stay open
	if m.form != "warp_log" {
		t.Fatalf("expected form to stay 'warp_log' on conflict, got %q", m.form)
	}
	if m.fields[0] != "debug" {
		t.Fatalf("expected user draft 'debug' to be preserved, got %q", m.fields[0])
	}
	if m.err == nil || !strings.Contains(m.err.Error(), "WARP logging settings changed elsewhere") {
		t.Fatalf("expected conflict message in err, got %v", m.err)
	}
	m.locale = i18n.TraditionalChinese
	if !strings.Contains(m.View(), "WARP 日誌設定已在其他地方變更；已更新修訂號，請按 Enter 重試。") {
		t.Fatalf("expected localized conflict message, got:\n%s", m.View())
	}

	// Model dispatched background fetch to refresh revision
	if conflictCmd == nil {
		t.Fatal("expected background refresh cmd on conflict")
	}

	// Background status update arrives while user is in form
	refreshMsg := conflictCmd()
	next, _ = m.Update(refreshMsg)
	m = next.(model)

	// Revision was updated to 'rev-refreshed' but draft 'debug' is still intact
	if m.warpLogStatus.Revision != "rev-refreshed" {
		t.Fatalf("expected revision 'rev-refreshed', got %q", m.warpLogStatus.Revision)
	}
	if m.fields[0] != "debug" {
		t.Fatalf("draft modified after revision refresh: got %q", m.fields[0])
	}

	// 2. Retry submit with Enter
	next, retryCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if retryCmd == nil {
		t.Fatal("expected retry submit cmd")
	}

	retryRes := retryCmd()
	next, _ = m.Update(retryRes)
	m = next.(model)

	// Second attempt succeeded and closed form
	if m.form != "" {
		t.Fatalf("expected form to close after successful retry, got %q", m.form)
	}
	if putCount != 2 {
		t.Fatalf("expected 2 PUT attempts, got %d", putCount)
	}
}

func TestWarpLogCancelWithEsc(t *testing.T) {
	m := model{
		ctx:    context.Background(),
		ready:  true,
		page:   3,
		width:  100,
		height: 40,
		form:   "warp_log",
		fields: []string{"warning"},
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)

	if m.form != "" {
		t.Fatalf("expected form to close on Esc, got %q", m.form)
	}
	if m.fields != nil {
		t.Fatalf("expected fields to be nil on cancel, got %v", m.fields)
	}
}

func TestTUICJKAndNarrowTerminalLayout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, err := control.NewClient(server.URL, "token", "")
	if err != nil {
		t.Fatal(err)
	}

	widths := []int{60, 72, 80, 120}
	locales := []i18n.Locale{i18n.English, i18n.TraditionalChinese}

	for _, width := range widths {
		for _, locale := range locales {
			m := model{
				ctx:               context.Background(),
				client:            client,
				locale:            locale,
				ready:             true,
				width:             width,
				height:            24,
				cfg:               config.Config{Outbounds: []config.Outbound{{ID: "direct", Type: "direct", Enabled: true}}},
				outboundRefresh:   30 * time.Second,
				refreshConfigured: true,
				warpLogStatus: resourcecontrol.WarpLogStatus{
					Supported:      true,
					Level:          resourcecontrol.WarpLogLevelInfo,
					EffectiveLevel: "info",
					Revision:       "rev-test",
				},
			}

			// Render Outbounds page (page 1)
			m.page = 1
			outboundsView := m.View()
			if len(outboundsView) == 0 {
				t.Fatalf("empty outbounds view at width %d with locale %v", width, locale)
			}

			// Render Settings page (page 3)
			m.page = 3
			settingsView := m.View()
			if len(settingsView) == 0 {
				t.Fatalf("empty settings view at width %d with locale %v", width, locale)
			}

			// Render WARP log-level form
			m.form = "warp_log"
			m.fields = []string{"info"}
			formView := m.View()
			if len(formView) == 0 {
				t.Fatalf("empty warp_log form view at width %d with locale %v", width, locale)
			}
		}
	}
}
