package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"rillway/internal/config"
	"rillway/internal/control"
	"rillway/internal/engine"
	"rillway/internal/i18n"
	"strings"
	"testing"
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
		{"settings", model{ready: true, page: 2}, []string{"[Service settings]", "Management UI", "Web UI token", "t Show / hide Web UI token", "Start or install the service on the machine running it"}},
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
	m := model{ready: true, page: 2, managementToken: token}
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
		for range 2 {
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
