package tui

import (
	"encoding/json"
	"os/exec"
	"rillway/internal/config"
	"rillway/internal/engine"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
	if !strings.Contains(m.View(), "已結束") {
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
	if !strings.Contains(m.View(), "上游未提供實際 IP") {
		t.Fatal("unknown upstream DNS not displayed")
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
