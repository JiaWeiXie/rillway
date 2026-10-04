package tui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/control"
	"rillway/internal/i18n"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func key(value string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)} }

func TestDisconnectedScreenAndRefresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer server.Close()
	client, err := control.NewClient(server.URL, "token", "")
	if err != nil {
		t.Fatal(err)
	}
	m := model{ctx: t.Context(), client: client, connection: ConnectionSettings{BaseURL: server.URL}, err: errors.New("connection refused")}
	view := m.View()
	if strings.Contains(view, "Configuration revision: 0") || strings.Contains(view, "No connections yet") || !strings.Contains(view, "Press o") {
		t.Fatal("offline UI pretends to have loaded configuration")
	}
	next, cmd := m.Update(key("r"))
	m = next.(model)
	if cmd == nil || !m.loading {
		t.Fatal("offline refresh still unavailable")
	}
	if result := cmd().(loadedMsg); result.err != nil {
		t.Fatal(result.err)
	}
	next, _ = m.Update(key("o"))
	m = next.(model)
	if m.form != "connection" || m.fields[0] != server.URL {
		t.Fatal("connection editor lost URL")
	}
	next, _ = m.Update(key("L"))
	m = next.(model)
	if m.locale == i18n.TraditionalChinese || !strings.HasSuffix(m.fields[0], "L") {
		t.Fatal("form consumed uppercase L")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	m = next.(model)
	if !strings.Contains(m.View(), "連到已啟動") || !strings.HasSuffix(m.fields[0], "L") {
		t.Fatal("form locale or input lost")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.(model).fields != nil {
		t.Fatal("cancel retained draft fields")
	}
}

func TestConnectionValidationAndStaleMessages(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("private-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := model{ctx: t.Context(), width: 100, connection: ConnectionSettings{BaseURL: "http://192.0.2.10:17892", TokenFile: token}}
	m.openConnection()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd != nil || m.err == nil || m.form != "connection" {
		t.Fatal("remote HTTP accepted")
	}
	if strings.Contains(m.View(), "private-token") {
		t.Fatal("token contents exposed")
	}
	m.fields[0] = "http://127.0.0.1:17892"
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil || m.generation != 1 || m.form != "" || !m.rememberPending {
		t.Fatal("valid connection not applied")
	}
	next, _ = m.Update(loadedMsg{cfg: config.Default(t.TempDir()), generation: 0})
	m = next.(model)
	if m.ready || m.cfg.Revision != 0 {
		t.Fatal("old connection overwrote new one")
	}
	next, _ = m.Update(statusesMsg{err: errors.New("old error"), generation: 0})
	if next.(model).err != nil {
		t.Fatal("old status error leaked into new connection")
	}
}

func TestOutboundFormDefaultsCancelAndServerFailure(t *testing.T) {
	c := config.Default(t.TempDir())
	m := model{ctx: t.Context(), ready: true, page: 1, cfg: c, width: 100}
	next, cmd := m.Update(key("+"))
	m = next.(model)
	if cmd != nil || m.form != "outbound" || m.fields[4] != "127.0.0.1:40000" || m.fields[5] != "warp-cli" || m.fields[1] != "warp-2" {
		t.Fatal("WARP form is not prefilled")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(model)
	if m.outDraft.Type != "wireguard" || m.fields[2] != "false" || m.fields[4] == "" {
		t.Fatal("WireGuard enabled without user file")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(model)
	if m.outDraft.Type != "tailscale" || m.fields[3] != "false" || m.fields[5] == "" {
		t.Fatal("Tailscale state not prefilled or public allowed")
	}
	m.field = 3
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if next.(model).fields[3] != "false" {
		t.Fatal("Tailscale public flag unlocked")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.form != "" || len(m.cfg.Outbounds) != 2 {
		t.Fatal("cancel changed configuration")
	}
	m.openOutbound()
	m.saving = true
	next, _ = m.Update(resultMsg{err: errors.New("revision conflict"), form: "outbound"})
	m = next.(model)
	if m.form != "outbound" || m.saving || m.fields[4] != "127.0.0.1:40000" {
		t.Fatal("server error discarded draft")
	}
	if editText("x👨‍👩‍👧‍👦", tea.KeyMsg{Type: tea.KeyBackspace}) != "x" {
		t.Fatal("field editing splits an emoji")
	}
}

func TestRememberOnlyAfterSuccessfulLoad(t *testing.T) {
	calls := 0
	m := model{rememberPending: true, remember: func(ConnectionSettings) error { calls++; return nil }}
	next, _ := m.Update(loadedMsg{err: errors.New("unauthorized")})
	m = next.(model)
	if calls != 0 {
		t.Fatal("unverified connection remembered")
	}
	next, _ = m.Update(loadedMsg{cfg: config.Default(t.TempDir())})
	m = next.(model)
	if calls != 1 || !m.ready {
		t.Fatal("verified connection not remembered")
	}
	_, _ = m.Update(loadedMsg{cfg: m.cfg})
	if calls != 1 {
		t.Fatal("periodic refresh rewrites profile")
	}
}

func TestOutboundSaveAndTypeDrafts(t *testing.T) {
	c := config.Default(t.TempDir())
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/config" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			return
		}
		calls++
		var saved config.Config
		if err := json.NewDecoder(r.Body).Decode(&saved); err != nil {
			t.Error(err)
			return
		}
		added := saved.Outbounds[len(saved.Outbounds)-1]
		if added.ProxyAddress != "127.0.0.1:49999" || added.WARPBinary != "warp-cli" || saved.Revision != c.Revision {
			t.Error("draft or revision was lost")
		}
		_ = json.NewEncoder(w).Encode(saved)
	}))
	defer server.Close()
	client, err := control.NewClient(server.URL, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	m := model{ctx: t.Context(), client: client, cfg: c, ready: true, width: 100, height: 30}
	m.openOutbound()
	m.fields[4] = "127.0.0.1:49999"
	for range 3 {
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRight})
		m = next.(model)
		if cmd != nil {
			t.Fatal("type selection caused request")
		}
	}
	if m.fields[4] != "127.0.0.1:49999" || calls != 0 {
		t.Fatal("switching types lost input or saved early")
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil || !m.saving || calls != 0 {
		t.Fatal("explicit save was not queued")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.(model).form != "outbound" {
		t.Fatal("pending save incorrectly cancelled")
	}
	result := cmd().(resultMsg)
	if result.err != nil || calls != 1 {
		t.Fatalf("save failed: %v", result.err)
	}
	next, _ = m.Update(result)
	if next.(model).form != "" {
		t.Fatal("successful save did not close form")
	}
}

func TestRemoteServiceControlsAndOfflineHelp(t *testing.T) {
	m := model{ctx: t.Context(), connection: ConnectionSettings{BaseURL: "https://192.0.2.10:17892"}, startCommand: func() *exec.Cmd { return exec.Command("false") }}
	next, cmd := m.Update(key("s"))
	if cmd != nil || next.(model).form == "start" {
		t.Fatal("remote service started on local machine")
	}
	next, _ = m.Update(key("?"))
	if !strings.Contains(next.View(), "First connect") || strings.Contains(next.View(), "http:///proxy.pac") {
		t.Fatal("offline help invents proxy addresses")
	}
	m.connection.BaseURL = "https://127.0.0.1:17892"
	next, cmd = m.Update(key("s"))
	if cmd != nil || next.(model).form != "start" {
		t.Fatal("local start skipped confirmation")
	}
	next, cmd = next.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil || next.(model).form != "" {
		t.Fatal("cancelled start executed")
	}
}

func TestConnectedServiceIdentity(t *testing.T) {
	m := model{ready: true, connection: ConnectionSettings{BaseURL: "https://192.0.2.10:17892"}}
	if !strings.Contains(m.View(), "Service: https://192.0.2.10:17892") {
		t.Fatal("connected service identity missing")
	}
}

func TestLocalServiceListeningOnLANKeepsLocalControls(t *testing.T) {
	m := model{localBaseURL: "https://192.0.2.10:17892", connection: ConnectionSettings{BaseURL: "https://192.0.2.10:17892"}}
	if !m.localTarget() {
		t.Fatal("installed local service using LAN IP lost local controls")
	}
	m.connection.BaseURL = "https://192.0.2.11:17892"
	if m.localTarget() {
		t.Fatal("switching to another VM kept local controls")
	}
}
