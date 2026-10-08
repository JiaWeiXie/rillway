package tui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"rillway/internal/access"
	"rillway/internal/config"
	"rillway/internal/control"
	"rillway/internal/engine"
	"rillway/internal/i18n"
	"strings"
	"testing"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rivo/uniseg"
)

func TestEngineSnapshotJSONContract(t *testing.T) {
	input := engine.Snapshot{Flows: []engine.Flow{{ID: 42, Host: "raw.githubusercontent.com", Port: "443", Closed: true, UploadRate: 128, DownloadRate: 2048}}}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var decoded snapshot
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Flows) != 1 || decoded.Flows[0].ID != 42 || decoded.Flows[0].Port != "443" || !decoded.Flows[0].Closed {
		t.Fatalf("bad engine snapshot: %+v", decoded)
	}
	m := model{ready: true, height: 30, flows: decoded.Flows}
	if !strings.Contains(m.View(), "Closed") {
		t.Fatal("closed flow rendered as active")
	}
}

func TestFlowRuleTargetsExactDomainOrAddress(t *testing.T) {
	for _, tc := range []struct {
		host, want string
		isIP       bool
	}{{"github.com", "github.com", false}, {"185.199.108.133", "185.199.108.133/32", true}, {"2606:50c0:8000::154", "2606:50c0:8000::154/128", true}} {
		r, err := ruleForFlow(flow{Host: tc.host}, "rule", "warp", "ipv6", nil)
		if err != nil {
			t.Fatal(err)
		}
		if r.Outbound != "warp" || r.Family != "ipv6" {
			t.Fatal("routing choice lost")
		}
		if tc.isIP {
			if len(r.CIDRs) != 1 || r.CIDRs[0] != tc.want {
				t.Fatalf("wrong CIDR: %+v", r)
			}
		} else if len(r.Domains) != 1 || r.Domains[0] != tc.want || len(r.Suffixes) != 0 {
			t.Fatalf("domain rule broadened: %+v", r)
		}
	}
	if _, err := ruleForFlow(flow{}, "rule", "direct", "", nil); err == nil {
		t.Fatal("empty target allowed")
	}
	if _, err := ruleForFlow(flow{Host: "example.com"}, "rule", "", "", nil); err == nil {
		t.Fatal("empty adaptive candidates allowed")
	}
	r, err := ruleForFlow(flow{Host: "example.com"}, "rule", "", "", []string{"direct", "warp"})
	if err != nil || !r.Adaptive || len(r.Candidates) != 2 {
		t.Fatal("adaptive rule not generated")
	}
}

func TestLicenseInputMaskedAndEscapeClears(t *testing.T) {
	m := model{form: "license", licenseID: "warp", input: "top-secret-license"}
	if strings.Contains(m.View(), m.input) {
		t.Fatal("license appears in terminal")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	state := next.(model)
	if state.input != "" || state.form != "" || state.licenseID != "" {
		t.Fatal("license retained after cancel")
	}
}

func TestNavigationAndFlowSelection(t *testing.T) {
	m := model{ready: true, height: 25, flows: []flow{{Host: "one.example"}, {Host: "two.example"}}, cfg: config.Config{Outbounds: []config.Outbound{{ID: "direct", Type: "direct"}}}}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(model)
	if m.selected != 1 {
		t.Fatal("selection did not move")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	if m.form != "rule" || m.ruleFlow.Host != "two.example" {
		t.Fatal("wrong target selected")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(model)
	if m.page != 1 || m.selected != 0 {
		t.Fatal("page selection not reset")
	}
}

func TestUnknownRemoteDNSIsNotInvented(t *testing.T) {
	m := model{ready: true, height: 30, flows: []flow{{Host: "github.com", Outbound: "warp"}}}
	if !strings.Contains(m.View(), "IP not reported by upstream") {
		t.Fatal("unknown upstream DNS not displayed")
	}
}

func TestEnglishNavigationAndForms(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model model
		want  []string
	}{
		{"loading", model{}, []string{"Connecting to the management service"}},
		{"connections", model{ready: true}, []string{"[Connections]", "No connections yet", "Enter Create routing rule"}},
		{"outbounds", model{ready: true, page: 1}, []string{"[Outbounds & VPNs]", "c Connect", "d Disconnect", "v Verify"}},
		{"sources", model{ready: true, page: 2}, []string{"[Source access]", "Clients", "v Toggle Clients / Rules view", "b Block selected client"}},
		{"settings", model{ready: true, page: 3}, []string{"[Service settings]", "Management UI", "Web UI token", "t Show / hide Web UI token", "Start or install the service on the machine running it"}},
		{"rule", model{form: "rule", ruleFlow: flow{Host: "example.com"}}, []string{"Create routing rule for example.com", "Automatic (dual stack)", "Enter Save", "Esc Cancel"}},
		{"license", model{form: "license"}, []string{"WARP+ license key", "Enter Apply", "Esc Cancel"}},
		{"install", model{form: "install"}, []string{"Install the Rillway background service", "Enter Install", "Esc Cancel"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := tc.model.View()
			for _, want := range append([]string{"≈ Rillway   Routing console"}, tc.want...) {
				if !strings.Contains(view, want) {
					t.Errorf("missing English control %q in:\n%s", want, view)
				}
			}
			// Fixtures have no user-provided text; this checks only the UI chrome.
			if strings.ContainsFunc(view, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
				t.Errorf("untranslated UI text in:\n%s", view)
			}
		})
	}
}

func TestManagementTokenRequiresExplicitReveal(t *testing.T) {
	const token = "private-management-token"
	m := model{ready: true, page: 3, managementToken: token}
	if view := m.View(); strings.Contains(view, token) || !strings.Contains(view, "Hidden (press t to show)") {
		t.Fatalf("token was exposed before reveal or hint is missing:\n%s", view)
	}

	next, command := m.Update(key("t"))
	m = next.(model)
	if command != nil || !m.tokenVisible || !strings.Contains(m.View(), token) {
		t.Fatal("explicit reveal did not show the management token")
	}

	next, _ = m.Update(key("L"))
	m = next.(model)
	if !strings.Contains(m.View(), "Web UI 權杖") || !strings.Contains(m.View(), token) {
		t.Fatal("Traditional Chinese view changed or hid the revealed token")
	}

	next, _ = m.Update(key("tab"))
	m = next.(model)
	if m.tokenVisible || strings.Contains(m.View(), token) {
		t.Fatal("leaving service settings did not hide the token")
	}

	m.page = 0
	next, _ = m.Update(key("t"))
	if next.(model).tokenVisible {
		t.Fatal("token shortcut worked outside service settings")
	}
}

func TestInstallCommandRequiresConfirmation(t *testing.T) {
	calls := 0
	m := model{installCommand: func() *exec.Cmd {
		calls++
		return exec.Command("rillway-test-never-executed")
	}}
	next, command := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	m = next.(model)
	if calls != 0 || command != nil || m.form != "install" {
		t.Fatal("installer started before confirmation")
	}
	next, command = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if calls != 0 || command != nil || m.form != "" {
		t.Fatal("cancel invoked installer")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	m = next.(model)
	next, command = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if calls != 1 || command == nil || next.(model).form != "" {
		t.Fatal("confirmation did not hand installer to Bubble Tea")
	}
}

func TestLanguageSwitchPreservesFormsAndUserText(t *testing.T) {
	for _, form := range []string{"", "rule", "license", "install"} {
		t.Run(form, func(t *testing.T) {
			m := model{
				ctx: t.Context(), ready: true, page: 1, form: form, input: "secret-L-🚀", licenseID: "公司👨‍👩‍👧‍👦", ruleChoice: 2, ruleFamily: 1,
				ruleFlow: flow{Host: "公司.example"}, cfg: config.Config{Outbounds: []config.Outbound{{ID: "公司👨‍👩‍👧‍👦", Type: "warp"}}},
			}
			for _, locale := range []i18n.Locale{i18n.TraditionalChinese, i18n.English} {
				key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}}
				if form == "license" {
					key = tea.KeyMsg{Type: tea.KeyCtrlL}
				}
				next, command := m.Update(key)
				m = next.(model)
				if command != nil || m.locale != locale || i18n.FromContext(m.ctx) != locale {
					t.Fatal("language did not switch without an action")
				}
				if m.form != form || m.input != "secret-L-🚀" || m.licenseID != "公司👨‍👩‍👧‍👦" || m.ruleChoice != 2 || m.ruleFamily != 1 || m.page != 1 {
					t.Fatal("switching language changed user input or navigation")
				}
				want := "Routing console"
				if locale == i18n.TraditionalChinese {
					want = "網路分流控制台"
				}
				if !strings.Contains(m.View(), want) || strings.Contains(m.View(), m.input) {
					t.Fatal("view language did not update or license leaked")
				}
			}
		})
	}
	m := model{ready: true, page: 1, cfg: config.Config{Outbounds: []config.Outbound{{ID: "warp", Type: "warp"}}}}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if next.(model).form != "license" {
		t.Fatal("lowercase l no longer opens the license form")
	}
	m = next.(model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	m = next.(model)
	if m.input != "L" || m.locale == i18n.TraditionalChinese {
		t.Fatal("uppercase L was consumed by the language shortcut")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	m = next.(model)
	if m.input != "L" || m.form != "license" || m.locale != i18n.TraditionalChinese || !strings.Contains(m.View(), "Ctrl+L 語言") {
		t.Fatal("Ctrl+L did not preserve license input and show the form shortcut")
	}
}

func TestLicenseBackspaceRemovesWholeGrapheme(t *testing.T) {
	m := model{form: "license", input: "x👨‍👩‍👧‍👦"}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if next.(model).input != "x" {
		t.Fatal("backspace split an emoji grapheme")
	}
}

func TestLanguageSwitchUpdatesAPIRequests(t *testing.T) {
	headers := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Get("Accept-Language")
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()
	client, err := control.NewClient(server.URL, "test-token", "")
	if err != nil {
		t.Fatal(err)
	}
	m := model{ctx: t.Context(), client: client}
	for _, locale := range []i18n.Locale{i18n.TraditionalChinese, i18n.English} {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
		m = next.(model)
		if result := m.load()().(loadedMsg); result.err != nil {
			t.Fatal(result.err)
		}
		for range 3 {
			if got := <-headers; got != string(locale) {
				t.Fatalf("Accept-Language = %q, want %q", got, locale)
			}
		}
	}
}

func TestAPIErrorFollowsLanguageSwitch(t *testing.T) {
	const source = "Enter a valid management token to sign in."
	chinese := i18n.Message(i18n.TraditionalChinese, source)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": chinese, "error_source": source})
	}))
	defer server.Close()
	client, err := control.NewClient(server.URL, "test-token", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := i18n.WithLocale(t.Context(), i18n.TraditionalChinese)
	_, err = client.Config(ctx)
	if err == nil {
		t.Fatal("API error was not returned")
	}
	m := model{ctx: ctx, locale: i18n.TraditionalChinese, err: err}
	for _, tc := range []struct {
		want, absent string
	}{{chinese, source}, {source, chinese}, {chinese, source}} {
		view := m.View()
		if !strings.Contains(view, tc.want) || strings.Contains(view, tc.absent) {
			t.Fatalf("API error did not follow locale %q: %s", m.locale, view)
		}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
		m = next.(model)
	}
	for _, source := range []string{"", "Unknown upstream diagnostic 🚀"} {
		apiError := &control.APIError{Status: http.StatusBadGateway, Message: "舊版伺服器訊息 🚀", Source: source}
		want := source
		if want == "" {
			want = apiError.Message
		}
		for _, locale := range []i18n.Locale{i18n.English, i18n.TraditionalChinese} {
			m.locale = locale
			if !strings.Contains(m.errorText(apiError), want) {
				t.Fatal("unknown or legacy error text was rewritten")
			}
		}
	}
}

func TestCellPreservesGraphemesAndDisplayWidth(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		width       int
	}{
		{"中文", "中文", 4},
		{"中文測試", "中… ", 4},
		{"👨‍👩‍👧‍👦🚀", "👨‍👩‍👧‍👦🚀", 4},
		{"👨‍👩‍👧‍👦🚀X", "👨‍👩‍👧‍👦… ", 4},
		{"🚀X", "… ", 2},
		{"e\u0301中文", "e\u0301… ", 3},
		{"🚀", "…", 1},
		{"中文", "", 0},
	} {
		if got := cell(tc.input, tc.width); got != tc.want || uniseg.StringWidth(got) != tc.width {
			t.Errorf("cell(%q, %d) = %q (width %d), want %q", tc.input, tc.width, got, uniseg.StringWidth(got), tc.want)
		}
	}
}

func TestRefreshPreservesSelectedDestination(t *testing.T) {
	m := model{ready: true, selected: 1, flows: []flow{{ID: 10, Host: "first.example"}, {ID: 20, Host: "selected.example"}}}
	updated, _ := m.Update(loadedMsg{snapshot: snapshot{Flows: []flow{{ID: 20, Host: "selected.example"}, {ID: 30, Host: "new.example"}}}})
	m = updated.(model)
	if m.selected != 0 {
		t.Fatal("refresh moved selection to a different connection")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.(model).ruleFlow.Host != "selected.example" {
		t.Fatal("routing editor opened for a different destination")
	}
	m = updated.(model)
	m.form = ""
	updated, _ = m.Update(loadedMsg{snapshot: snapshot{Flows: nil}})
	m = updated.(model)
	if m.selected != 0 {
		t.Fatal("empty refresh left an invalid selection")
	}
	_ = m.View()
}

func TestRefreshPreservesSelectedOutbound(t *testing.T) {
	m := model{ready: true, page: 1, selected: 1, cfg: config.Config{Outbounds: []config.Outbound{{ID: "direct"}, {ID: "warp"}}}}
	updated, _ := m.Update(loadedMsg{cfg: config.Config{Outbounds: []config.Outbound{{ID: "warp"}, {ID: "direct"}}}})
	m = updated.(model)
	if m.cfg.Outbounds[m.selected].ID != "warp" {
		t.Fatal("refresh moved selection to a different outbound")
	}
}

func TestInFlightRefreshDoesNotReplaceOpenEditor(t *testing.T) {
	for _, form := range []string{"rule", "outbound"} {
		t.Run(form, func(t *testing.T) {
			original := config.Config{Revision: 7, Outbounds: []config.Outbound{{ID: "direct"}, {ID: "warp"}}}
			m := model{ready: true, loading: true, form: form, cfg: original, ruleChoice: 1}
			next, _ := m.Update(loadedMsg{cfg: config.Config{Revision: 8, Outbounds: []config.Outbound{{ID: "direct"}}}})
			m = next.(model)
			if m.loading || m.cfg.Revision != 7 || len(m.cfg.Outbounds) != 2 || m.cfg.Outbounds[m.ruleChoice].ID != "warp" {
				t.Fatal("in-flight refresh changed the editor revision or routing choice")
			}
			m.form = ""
			next, _ = m.Update(loadedMsg{cfg: config.Config{Revision: 8, Outbounds: []config.Outbound{{ID: "direct"}}}})
			if next.(model).cfg.Revision != 8 {
				t.Fatal("refresh did not resume after closing the editor")
			}
		})
	}
}

func TestServerVersionAndAdvancedRevision(t *testing.T) {
	for _, locale := range []i18n.Locale{i18n.English, i18n.TraditionalChinese} {
		m := model{ready: true, locale: locale, cfg: config.Config{Revision: 42}, serverInfo: control.Info{Version: "v9.8.7"}, height: 40}
		if view := m.View(); !strings.Contains(view, "v9.8.7") || strings.Contains(view, "42") {
			t.Fatalf("unexpected main version display: %s", view)
		}
		m.page = 3
		next, _ := m.Update(key("x"))
		m = next.(model)
		if !m.advancedVisible || !strings.Contains(m.View(), "42") {
			t.Fatal("advanced revision did not open")
		}
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = next.(model)
		if m.advancedVisible || strings.Contains(m.View(), "42") {
			t.Fatal("advanced information remained open after leaving settings")
		}
		m.serverInfo = control.Info{}
		if strings.Contains(m.View(), "v9.8.7") || !strings.Contains(m.View(), m.text("\n  Program version unavailable\n")) {
			t.Fatal("unknown server version mislabeled")
		}
	}
}

func TestLoadUsesRemoteVersionAndOnlyFallsBackForOlderDaemons(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/config":
					_, _ = w.Write([]byte(`{"revision":42}`))
				case "/api/v1/stats":
					_, _ = w.Write([]byte(`{"flows":[]}`))
				case "/api/v1/info":
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"version":"v9.8.7"}`))
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
			}))
			defer server.Close()
			client, err := control.NewClient(server.URL, "test-token", "")
			if err != nil {
				t.Fatal(err)
			}
			m := model{ctx: context.Background(), client: client, generation: 2}
			loaded := m.load()().(loadedMsg)
			if status == http.StatusOK && (loaded.err != nil || loaded.info.Version != "v9.8.7") {
				t.Fatalf("remote version missing: %+v", loaded)
			}
			if status == http.StatusNotFound && (loaded.err != nil || loaded.info.Version != "") {
				t.Fatalf("older daemon fallback failed: %+v", loaded)
			}
			if status >= 400 && status != http.StatusNotFound && loaded.err == nil {
				t.Fatal("metadata failure hidden")
			}
			next, _ := m.Update(loadedMsg{generation: 1, info: control.Info{Version: "stale"}})
			if next.(model).serverInfo.Version != "" {
				t.Fatal("old service metadata applied")
			}
		})
	}
}

func TestSourceAccessTUIWorkflow(t *testing.T) {
	snap := access.SourceSnapshot{
		GeneratedAt: time.Now(),
		Clients: []access.SourceClient{
			{
				Address:             "192.168.1.50",
				Decision:            "allow",
				ActiveConnections:   3,
				AcceptedConnections: 10,
				DeniedConnections:   0,
				LastSeen:            time.Now(),
			},
		},
	}
	rule := config.SourceAccessRule{
		ID:      "rule-1",
		Name:    "Office LAN",
		Action:  "allow",
		CIDRs:   []string{"192.168.1.0/24"},
		Enabled: true,
	}
	cfg := config.Config{
		Revision: 10,
		SourceAccess: config.SourceAccess{
			Rules: []config.SourceAccessRule{rule},
		},
	}

	m := model{
		ready:          true,
		page:           2,
		cfg:            cfg,
		sourceSnapshot: snap,
		width:          100,
		height:         30,
	}

	// 1. 預設在 Clients view
	view := m.View()
	if !strings.Contains(view, "192.168.1.50") || !strings.Contains(view, "Clients") {
		t.Fatalf("Clients view missing client: %s", view)
	}

	// 2. 按 'b' 開啟 Block client 對話框
	next, _ := m.Update(key("b"))
	m = next.(model)
	if m.form != "block_source" || !strings.Contains(m.View(), "Block source client 192.168.1.50") {
		t.Fatalf("Block dialog not shown: %s", m.View())
	}
	// Esc 取消
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	if m.form != "" {
		t.Fatalf("Form not closed on Esc: %s", m.form)
	}

	// 3. 按 'v' 切換到 Rules view
	next, _ = m.Update(key("v"))
	m = next.(model)
	if !m.sourceViewRules {
		t.Fatal("v did not toggle to rules view")
	}
	view = m.View()
	if !strings.Contains(view, "Office LAN") || !strings.Contains(view, "192.168.1.0/24") {
		t.Fatalf("Rules view missing rule: %s", view)
	}

	// 4. 按 '+' 開啟新增來源規則表單
	next, _ = m.Update(key("+"))
	m = next.(model)
	if m.form != "source_rule" || m.editingSourceRule != "" {
		t.Fatalf("Add source rule form not opened: %s", m.form)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)

	// 5. 按 'e' 編輯已選取的規則
	next, _ = m.Update(key("e"))
	m = next.(model)
	if m.form != "source_rule" || m.editingSourceRule != "rule-1" {
		t.Fatalf("Edit source rule form not opened: %s", m.form)
	}
	if m.fields[1] != "Office LAN" {
		t.Fatalf("Edit form did not load rule name: %+v", m.fields)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)

	// 6. 按 'd' 開啟刪除確認對話框
	next, _ = m.Update(key("d"))
	m = next.(model)
	if m.form != "delete_source_rule" || !strings.Contains(m.View(), "Delete source rule Office LAN") {
		t.Fatalf("Delete rule dialog not shown: %s", m.View())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(model)
	// 7. 切換為繁體中文，驗證 Rules 與 Clients 檢視表頭完全翻譯
	next, _ = m.Update(key("L"))
	m = next.(model)
	viewZH := m.View()
	for _, wantZH := range []string{"規則", "動作", "IP／CIDR 列表", "啟用"} {
		if !strings.Contains(viewZH, wantZH) {
			t.Fatalf("Traditional Chinese Rules view missing %q: %s", wantZH, viewZH)
		}
	}
	// 切回 Clients 檢視
	next, _ = m.Update(key("v"))
	m = next.(model)
	viewClientsZH := m.View()
	for _, wantClientsZH := range []string{"用戶端", "位址", "狀態", "連線數", "累計連線", "最近活躍"} {
		if !strings.Contains(viewClientsZH, wantClientsZH) {
			t.Fatalf("Traditional Chinese Clients view missing %q: %s", wantClientsZH, viewClientsZH)
		}
	}

	// 8. 驗證 Client 列表在重新排序/刷新後選中項正確保留
	clientA := access.SourceClient{Address: "10.0.0.1", LastSeen: time.Now().Add(-time.Minute)}
	clientB := access.SourceClient{Address: "10.0.0.2", LastSeen: time.Now().Add(-time.Second)}
	m.sourceSnapshot = access.SourceSnapshot{
		Clients: []access.SourceClient{clientA, clientB},
	}
	m.selected = 1 // 選中 clientB (10.0.0.2)
	m.sourceViewRules = false

	// 當 snapshot 重新排序（例如 clientB 的活躍度提升被排到 index 0）
	reorderedSnapshot := access.SourceSnapshot{
		Clients: []access.SourceClient{clientB, clientA},
	}
	next, _ = m.Update(sourceSnapshotMsg{snapshot: reorderedSnapshot, generation: m.generation})
	m = next.(model)
	// 驗證 m.selected 追蹤到新的 index 0（對應 10.0.0.2）
	if m.selected != 0 || m.sourceSnapshot.Clients[m.selected].Address != "10.0.0.2" {
		t.Fatalf("selected client address not preserved across reordered snapshot: selected=%d, addr=%s", m.selected, m.sourceSnapshot.Clients[m.selected].Address)
	}

	// 9. 驗證 Rules 列表在重新排序/刷新後選中項正確保留
	ruleA := config.SourceAccessRule{ID: "rule-a", Name: "Rule A"}
	ruleB := config.SourceAccessRule{ID: "rule-b", Name: "Rule B"}
	m.cfg.SourceAccess.Rules = []config.SourceAccessRule{ruleA, ruleB}
	m.sourceViewRules = true
	m.selected = 1 // 選中 rule-b

	// 模擬 config 更新，rule-b 被排到第 0 位
	reorderedCfg := m.cfg
	reorderedCfg.SourceAccess.Rules = []config.SourceAccessRule{ruleB, ruleA}
	next, _ = m.Update(loadedMsg{cfg: reorderedCfg, generation: m.generation})
	m = next.(model)
	if m.selected != 0 || m.cfg.SourceAccess.Rules[m.selected].ID != "rule-b" {
		t.Fatalf("selected rule ID not preserved across reordered config: selected=%d, id=%s", m.selected, m.cfg.SourceAccess.Rules[m.selected].ID)
	}
}

func TestBlockIPv6DialogAndConflictPreservesForm(t *testing.T) {
	// 1. 驗證 IPv6 client 的 block 對話框顯示精確的 /128 CIDR
	m := model{
		ready: true,
		page:  2,
		sourceSnapshot: access.SourceSnapshot{
			Clients: []access.SourceClient{
				{Address: "2001:db8::1", ActiveConnections: 2},
			},
		},
	}
	next, _ := m.Update(key("b"))
	m = next.(model)
	if m.form != "block_source" || !strings.Contains(m.View(), "2001:db8::1/128") {
		t.Fatalf("IPv6 block dialog should show /128 prefix: %s", m.View())
	}

	// 2. 模擬 Block 遇到 409 衝突錯誤（例如由伺服器返回衝突）
	conflictErr := &control.APIError{Status: http.StatusConflict, Message: "configuration revision conflict", Source: "configuration revision conflict"}
	next, cmd := m.Update(blockSourceMsg{err: conflictErr, generation: m.generation})
	m = next.(model)
	// 驗證表單依然保持開啟，沒有被提前關閉，且錯誤被妥善記錄
	if cmd != nil {
		t.Fatal("unexpected command on block error")
	}
	if m.form != "block_source" {
		t.Fatalf("expected block_source form to remain open on 409 error, got %q", m.form)
	}
	if m.err != conflictErr {
		t.Fatalf("error was not retained: %+v", m.err)
	}
	view := m.View()
	if !strings.Contains(view, "configuration revision conflict") {
		t.Fatalf("error text not displayed in dialog view: %s", view)
	}
}

func TestBlockSuccessMessageFollowsLanguageSwitch(t *testing.T) {
	m := model{
		ready:  true,
		page:   2,
		locale: i18n.English,
	}

	// 模擬成功收到 blockSourceMsg，中斷了 3 條連線
	result := control.BlockSourceResult{
		Disconnected: 3,
		Config:       config.Config{Revision: 10},
	}
	next, _ := m.Update(blockSourceMsg{result: result, generation: m.generation})
	m = next.(model)

	// 1. 英文環境下檢查渲染出完整的已格式化英文提示
	viewEN := m.View()
	wantEN := "Source client blocked (3 connection(s) disconnected)."
	if !strings.Contains(viewEN, wantEN) {
		t.Fatalf("English view missing formatted success message: %s", viewEN)
	}

	// 2. 切換語言為繁體中文，驗證動態數字與繁體中文模板正確翻譯
	next, _ = m.Update(key("L"))
	m = next.(model)
	viewZH := m.View()
	wantZH := "用戶端已封鎖（已中斷 3 條連線）。"
	if !strings.Contains(viewZH, wantZH) {
		t.Fatalf("Traditional Chinese view missing formatted success message: %s", viewZH)
	}
	if strings.Contains(viewZH, wantEN) {
		t.Fatalf("Traditional Chinese view retained English formatted text: %s", viewZH)
	}
}

func TestSourceSnapshotDoesNotReplaceOpenForms(t *testing.T) {
	for _, form := range []string{"source_rule", "block_source", "delete_source_rule"} {
		t.Run(form, func(t *testing.T) {
			existing := access.SourceSnapshot{Clients: []access.SourceClient{{Address: "192.0.2.1", ActiveConnections: 2}}}
			retained := errors.New("retained form error")
			m := model{ready: true, page: 2, form: form, sourceLoading: true, sourceSnapshot: existing, err: retained}
			next, _ := m.Update(sourceSnapshotMsg{snapshot: access.SourceSnapshot{Clients: []access.SourceClient{{Address: "192.0.2.2"}}}, err: errors.New("background error")})
			m = next.(model)
			if len(m.sourceSnapshot.Clients) != 1 || m.sourceSnapshot.Clients[0].Address != "192.0.2.1" || m.sourceSnapshot.Clients[0].ActiveConnections != 2 || m.err != retained || m.sourceLoading {
				t.Fatalf("background refresh disturbed %s: %+v", form, m)
			}
		})
	}
}

func TestSourceAccessRowsRespectTerminalWidth(t *testing.T) {
	for _, width := range []int{40, 52, 90} {
		for _, locale := range []i18n.Locale{i18n.English, i18n.TraditionalChinese} {
			m := model{width: width, height: 30, locale: locale, sourceSnapshot: access.SourceSnapshot{Clients: []access.SourceClient{{Address: "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff", ActiveConnections: 2}}}, cfg: config.Config{SourceAccess: config.SourceAccess{Rules: []config.SourceAccessRule{{ID: "office", Name: "公司 👨‍👩‍👧‍👦 的來源規則", CIDRs: []string{"2001:db8:ffff:ffff:ffff:ffff:ffff:ffff/128", "192.0.2.0/24"}}}}}}
			for _, rules := range []bool{false, true} {
				m.sourceViewRules = rules
				for _, line := range strings.Split(m.sourcesView(), "\n") {
					if uniseg.StringWidth(line) > width {
						t.Fatalf("source row overflow at width %d locale %s: %q", width, locale, line)
					}
				}
			}
		}
	}
}
