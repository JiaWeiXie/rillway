package tui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"rillway/internal/app"
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
	if cmd == nil || m.generation != 1 || m.form != "" || !m.rememberPending || m.managementToken != "private-token" || m.tokenVisible {
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

func TestSourceRuleFormEditingAndValidation(t *testing.T) {
	m := model{ready: true, page: 2, cfg: config.Default(t.TempDir())}
	// 按 '+' 開啟表單
	next, _ := m.Update(key("+"))
	m = next.(model)
	if m.form != "source_rule" {
		t.Fatalf("expected source_rule form, got %s", m.form)
	}
	// 嘗試送出空名稱，應觸發驗證錯誤而非儲存
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd != nil || m.err == nil {
		t.Fatal("blank rule name should produce a validation error")
	}
	// 左右鍵切換 action
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab}) // 切到 action
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(model)
	if m.fields[2] != "deny" {
		t.Fatalf("expected deny action, got %s", m.fields[2])
	}
	// 按 Esc 取消，確認 draft fields 被清除
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.form != "" || m.fields != nil {
		t.Fatal("Esc did not cancel and clear source rule draft")
	}
}

func TestSourceRuleDraftPreservedAcrossLanguageSwitch(t *testing.T) {
	m := model{ready: true, page: 2, width: 100, height: 30}
	// 1. 開啟新增規則表單
	next, _ := m.Update(key("+"))
	m = next.(model)
	m.field = 1

	// 2. 輸入部分草稿內容
	for _, r := range "My Custom Office Rule" {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(model)
	}
	if m.fields[1] != "My Custom Office Rule" {
		t.Fatalf("draft text not entered: %q", m.fields[1])
	}

	// 3. 按 Ctrl+L 切換語言至繁體中文
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	m = next.(model)
	if m.locale != i18n.TraditionalChinese {
		t.Fatalf("locale not switched: %s", m.locale)
	}

	// 4. 驗證草稿資料完整保留，表單依然處於開啟狀態
	if m.form != "source_rule" || m.fields[1] != "My Custom Office Rule" {
		t.Fatalf("draft lost on language switch: form=%s, fields=%+v", m.form, m.fields)
	}
	viewZH := m.View()
	if !strings.Contains(viewZH, "新增來源存取規則") || !strings.Contains(viewZH, "My Custom Office Rule") {
		t.Fatalf("view in Traditional Chinese did not render populated draft: %s", viewZH)
	}
	// 驗證動作欄位顯示為「允許」，絕不包含未翻譯的 "allow"
	if !strings.Contains(viewZH, "允許") || strings.Contains(viewZH, "allow") {
		t.Fatalf("action should be translated to 允許 without English allow: %s", viewZH)
	}
	// 驗證底部按鍵說明為「Enter 儲存」，絕不包含「儲存／連線」或「Save / Connect」
	if !strings.Contains(viewZH, "Enter 儲存") || strings.Contains(viewZH, "儲存／連線") || strings.Contains(viewZH, "Save / Connect") {
		t.Fatalf("footer should be Enter 儲存 without 儲存／連線: %s", viewZH)
	}

	// 4b. 切換 action 為 deny，驗證顯示為「拒絕」，絕不包含 "deny"
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab}) // 切到 action
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight}) // 切換為 deny
	m = next.(model)
	viewDenyZH := m.View()
	if !strings.Contains(viewDenyZH, "拒絕") || strings.Contains(viewDenyZH, "deny") {
		t.Fatalf("action should be translated to 拒絕 without English deny: %s", viewDenyZH)
	}
	// 5. 再次切回英文，驗證草稿依然保留
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	m = next.(model)
	if m.locale != i18n.English || m.fields[1] != "My Custom Office Rule" {
		t.Fatalf("draft lost on switching back to English: %+v", m.fields)
	}
}

func TestSourceRuleIDNoteSaveAndConflict(t *testing.T) {
	c := config.Default(t.TempDir())
	path := filepath.Join(t.TempDir(), "config.json")
	runtime, err := app.New(t.Context(), path, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	server := httptest.NewServer(control.New(runtime, "token"))
	defer server.Close()
	client, err := control.NewClient(server.URL, "token", "")
	if err != nil {
		t.Fatal(err)
	}
	m := model{ctx: t.Context(), client: client, ready: true, page: 2, cfg: c, width: 100, height: 30}
	m.openAddSourceRule()
	values := []string{"office-local", "辦公室 👨‍👩‍👧‍👦", "deny", "192.0.2.0/24", "true", "公司網段備註 😀"}
	for i, value := range values {
		m.field = i
		if i == 2 || i == 4 {
			continue
		}
		for _, r := range value {
			msg := key(string(r))
			if r == ' ' {
				msg = tea.KeyMsg{Type: tea.KeySpace}
			}
			next, _ := m.Update(msg)
			m = next.(model)
		}
	}
	m.field = 2
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = next.(model)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if cmd == nil || m.err != nil {
		t.Fatalf("valid ID and note cannot save: %v", m.err)
	}
	result := cmd().(resultMsg)
	if result.err != nil {
		t.Fatal(result.err)
	}
	saved := runtime.Config()
	rule := saved.SourceAccess.Rules[len(saved.SourceAccess.Rules)-1]
	if rule.ID != values[0] || rule.Name != values[1] || rule.Note != values[5] || rule.Action != "deny" || !rule.Enabled {
		t.Fatalf("source fields lost: %+v", rule)
	}
	m.cfg = saved
	m.saving = false
	m.openEditSourceRule(rule)
	m.field = 5
	next, _ = m.Update(key(" updated"))
	m = next.(model)
	newer := runtime.Config()
	newer.SourceAccess.Rules[0].Note = "external edit"
	if err := runtime.Apply(t.Context(), newer); err != nil {
		t.Fatal(err)
	}
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	var conflict *control.APIError
	if !errors.As(m.err, &conflict) || conflict.Status != http.StatusConflict || m.form != "source_rule" || m.cfg.Revision != saved.Revision || m.fields[5] != values[5]+" updated" {
		t.Fatalf("conflict discarded note or revision: %+v %v", m.fields, m.err)
	}
}
