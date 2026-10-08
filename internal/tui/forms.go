package tui

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"rillway/internal/config"
	"rillway/internal/control"
	"rillway/internal/memorylimit"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rivo/uniseg"
)

// ConnectionSettings remembers file references only, never token contents.
type ConnectionSettings struct {
	BaseURL   string `json:"url"`
	TokenFile string `json:"token_file"`
	CAFile    string `json:"ca_file"`
}

func (m *model) openConnection() {
	m.form = "connection"
	m.tokenVisible = false
	m.field = 0
	m.fields = []string{m.connection.BaseURL, m.connection.TokenFile, m.connection.CAFile}
}

func (m *model) openOutbound() {
	m.form = "outbound"
	m.field = 0
	m.outType = 0
	m.drafts = map[string][]string{}
	m.outDraft = config.DefaultsForForms(m.cfg).Outbounds["warp"]
	m.fillOutboundFields()
	m.err = nil
}

func (m *model) fillOutboundFields() {
	o := m.outDraft
	m.fields = []string{o.Type, o.ID, fmt.Sprint(o.Enabled), fmt.Sprint(o.PublicInternet)}
	switch o.Type {
	case "warp":
		m.fields = append(m.fields, o.ProxyAddress, o.WARPBinary)
	case "wireguard":
		m.fields = append(m.fields, o.ConfigFile, strings.Join(o.DNS, ","))
	case "tailscale":
		m.fields = append(m.fields, o.Hostname, o.StateDir, o.AuthKeyFile, strings.Join(o.DNS, ","))
	}
}

func (m model) fieldLabels() []string {
	if m.form == "memory" {
		return []string{"Unit (Left/Right to choose)", "Memory value"}
	}
	if m.form == "connection" {
		return []string{"Management HTTPS URL", "Management token file", "Trusted certificate file"}
	}
	if m.form == "source_rule" {
		return []string{"Rule ID", "Rule name", "Action (Left/Right to choose)", "IP / CIDR list (comma separated)", "Enabled (Left/Right to toggle)", "Note (optional)"}
	}
	labels := []string{"Type (Left/Right to choose)", "Name / ID", "Enabled (Left/Right to change)", "Public Internet (Left/Right to change)"}
	switch m.outDraft.Type {
	case "warp":
		return append(labels, "WARP proxy address", "warp-cli command")
	case "wireguard":
		return append(labels, "WireGuard file on the server", "DNS servers (optional)")
	default:
		return append(labels, "Tailscale node name", "Private state folder on the server", "Auth key file (optional)", "DNS servers (optional)")
	}
}

func editText(value string, key tea.KeyMsg) string {
	switch key.String() {
	case "ctrl+u":
		return ""
	case "backspace", "ctrl+h":
		graphemes := uniseg.NewGraphemes(value)
		last := 0
		for graphemes.Next() {
			last, _ = graphemes.Positions()
		}
		return value[:last]
	case " ":
		if len(value) < 4096 {
			return value + " "
		}
	default:
		if key.Type == tea.KeyRunes && len(value) < 4096 {
			for _, r := range key.Runes {
				if !unicode.IsControl(r) {
					value += string(r)
				}
			}
		}
	}
	return value
}

func localFile(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, value[2:])
	}
	return filepath.Abs(value)
}

func (m model) updateFields(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.saving {
		return m, nil
	}
	switch key.String() {
	case "tab", "down":
		m.field = (m.field + 1) % len(m.fields)
	case "shift+tab", "up":
		m.field = (m.field + len(m.fields) - 1) % len(m.fields)
	case "enter":
		if m.form == "memory" {
			req := memorylimit.Request{Mode: m.fields[0], Value: strings.TrimSpace(m.fields[1]), Revision: m.memoryStatus.Revision}
			if _, err := memorylimit.Calculate(req, m.memoryStatus); err != nil {
				m.err = err
				return m, nil
			}
			m.saving, m.err = true, nil
			return m, func() tea.Msg {
				_, err := m.client.ApplyMemory(m.ctx, req)
				return resultMsg{generation: m.generation, form: "memory", message: "Memory limit saved. Applied without restarting the service.", err: err}
			}
		}
		if m.form == "connection" {
			settings := ConnectionSettings{BaseURL: strings.TrimSpace(m.fields[0])}
			var err error
			settings.TokenFile, err = localFile(m.fields[1])
			if err == nil {
				settings.CAFile, err = localFile(m.fields[2])
			}
			if err != nil {
				m.err = err
				return m, nil
			}
			token, err := os.ReadFile(settings.TokenFile)
			if err != nil {
				m.err = fmt.Errorf("could not read the management token file: %w", err)
				return m, nil
			}
			managementToken := strings.TrimSpace(string(token))
			client, err := control.NewClient(settings.BaseURL, managementToken, settings.CAFile)
			if err != nil {
				m.err = err
				return m, nil
			}
			m.client = client
			m.connection = settings
			m.managementToken = managementToken
			m.tokenVisible = false
			m.advancedVisible = false
			m.serverInfo = control.Info{}
			m.generation++
			m.ready = false
			m.loading = true
			m.statusLoading = true
			m.err = nil
			m.form = ""
			m.rememberPending = true
			return m, tea.Batch(m.load(), m.loadStatuses())
		}
		if m.form == "source_rule" {
			rule := config.SourceAccessRule{ID: strings.TrimSpace(m.fields[0]), Name: strings.TrimSpace(m.fields[1]), Action: m.fields[2], CIDRs: splitValues(m.fields[3]), Enabled: m.fields[4] == "true", Note: strings.TrimSpace(m.fields[5])}
			cfg := clone(m.cfg)
			if m.editingSourceRule != "" {
				found := false
				for i, current := range cfg.SourceAccess.Rules {
					if current.ID == m.editingSourceRule {
						cfg.SourceAccess.Rules[i] = rule
						found = true
						break
					}
				}
				if !found {
					m.err = config.PublicError{Message: "Source access rule no longer exists. Refresh before editing."}
					return m, nil
				}
			} else {
				cfg.SourceAccess.Rules = append(cfg.SourceAccess.Rules, rule)
			}
			if err := config.Validate(cfg); err != nil {
				m.err = config.PublicError{Message: "Source access configuration is invalid. Check rule IDs, names, actions and CIDR ranges.", Err: err}
				return m, nil
			}
			m.saving, m.err = true, nil
			return m, m.applyForm(cfg)
		}
		o := m.outDraft
		o.ID = strings.TrimSpace(m.fields[1])
		o.Enabled = m.fields[2] == "true"
		o.PublicInternet = m.fields[3] == "true"
		switch o.Type {
		case "warp":
			o.ProxyAddress = strings.TrimSpace(m.fields[4])
			o.WARPBinary = strings.TrimSpace(m.fields[5])
		case "wireguard":
			o.ConfigFile = strings.TrimSpace(m.fields[4])
			o.DNS = splitValues(m.fields[5])
		case "tailscale":
			o.Hostname = strings.TrimSpace(m.fields[4])
			o.StateDir = strings.TrimSpace(m.fields[5])
			o.AuthKeyFile = strings.TrimSpace(m.fields[6])
			o.DNS = splitValues(m.fields[7])
			o.PublicInternet = false
		}
		cfg := clone(m.cfg)
		cfg.Outbounds = append(cfg.Outbounds, o)
		if err := config.Validate(cfg); err != nil {
			m.err = err
			return m, nil
		}
		m.saving = true
		m.err = nil
		return m, m.applyForm(cfg)
	case "left", "right", " ":
		if m.form == "memory" && m.field == 0 {
			m.fields[0], m.fields[1] = nextMemoryUnit(m.fields[0], m.fields[1], m.memoryStatus, key.String() == "left")
		} else if m.form == "outbound" && m.field == 0 {
			m.drafts[m.outDraft.Type] = append([]string(nil), m.fields...)
			direction := 1
			if key.String() == "left" {
				direction = 2
			}
			m.outType = (m.outType + direction) % 3
			m.outDraft = config.DefaultsForForms(m.cfg).Outbounds[[]string{"warp", "wireguard", "tailscale"}[m.outType]]
			m.fillOutboundFields()
			if saved, ok := m.drafts[m.outDraft.Type]; ok {
				m.fields = append([]string(nil), saved...)
			}
			m.err = nil
		} else if m.form == "outbound" && (m.field == 2 || m.field == 3) {
			if m.field != 3 || m.outDraft.Type != "tailscale" {
				m.fields[m.field] = fmt.Sprint(m.fields[m.field] != "true")
			}
		} else if m.form == "source_rule" && m.field == 2 {
			if m.fields[2] == "allow" {
				m.fields[2] = "deny"
			} else {
				m.fields[2] = "allow"
			}
		} else if m.form == "source_rule" && m.field == 4 {
			m.fields[4] = fmt.Sprint(m.fields[4] != "true")
		} else {
			m.fields[m.field] = editText(m.fields[m.field], key)
		}
	default:
		if m.form == "connection" || m.form == "memory" && m.field == 1 || (m.form == "source_rule" && (m.field == 0 || m.field == 1 || m.field == 3 || m.field == 5)) || (m.form == "outbound" && m.field != 0 && m.field != 2 && m.field != 3) {
			m.fields[m.field] = editText(m.fields[m.field], key)
		}
	}
	return m, nil
}

func splitValues(value string) []string {
	var values []string
	for _, v := range strings.Split(value, ",") {
		if v = strings.TrimSpace(v); v != "" {
			values = append(values, v)
		}
	}
	return values
}

func (m model) applyForm(cfg config.Config) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.Apply(m.ctx, cfg)
		return resultMsg{message: "Configuration saved. Applies to new connections only.", err: err, generation: m.generation, form: m.form}
	}
}

func (m model) fieldsView() string {
	var b strings.Builder
	switch m.form {
	case "connection":
		b.WriteString(m.text("\n  Connect to a running Rillway service\n  This terminal manages the service; it does not start a proxy by itself.\n  For a VM, enter its HTTPS address and local token/certificate file paths.\n\n"))
	case "memory":
		b.WriteString(m.text("\n  Service memory limit · applies without restarting\n  Left/Right chooses percent, MiB or GiB. Enter saves.\n"))
		b.WriteString(m.memoryView())
	case "source_rule":
		if m.editingSourceRule != "" {
			b.WriteString(m.text("\n  Edit source access rule\n\n"))
		} else {
			b.WriteString(m.text("\n  Add source access rule\n\n"))
		}
	default:
		b.WriteString(m.text("\n  Add an outbound · common values are already filled in\n  File paths below belong to the Rillway server.\n\n"))
	}
	labels := m.fieldLabels()
	limit := max(2, (m.height-16)/2)
	start := max(0, m.field-limit+1)
	end := min(len(labels), start+limit)
	for i := start; i < end; i++ {
		label := labels[i]
		marker := " "
		if i == m.field {
			marker = "›"
		}
		value := m.fields[i]
		if value == "true" {
			value = m.text("Enabled")
		}
		if value == "false" {
			value = m.text("Disabled")
		}
		if m.form == "source_rule" && i == 2 {
			switch value {
			case "allow":
				value = m.text("Allowed")
			case "deny":
				value = m.text("Denied")
			}
		}
		if i == m.field {
			value += "▏"
		}
		fmt.Fprintf(&b, "  %s %s\n    %s\n", marker, m.text(label), cell(value, max(20, m.width-6)))
	}
	if m.form == "source_rule" {
		b.WriteString(m.text("\n  Tab / ↑↓ Next field   Ctrl+U Clear field   Enter Save   Esc Cancel\n"))
	} else {
		b.WriteString(m.text("\n  Tab / ↑↓ Next field   Ctrl+U Clear field   Enter Save / Connect   Esc Cancel\n"))
	}
	if m.form == "outbound" {
		switch m.outDraft.Type {
		case "warp":
			b.WriteString(m.text("  Save first, then select this outbound and press n Register, c Connect, v Verify.\n"))
		case "wireguard":
			b.WriteString(m.text("  Provide your WireGuard file before enabling. No keys are generated.\n"))
		case "tailscale":
			b.WriteString(m.text("  Enable and save to get a sign-in link. Public Internet access stays off.\n"))
		}
	}
	if m.saving {
		b.WriteString(m.text("  Saving…\n"))
	}
	if m.err != nil {
		fmt.Fprintf(&b, m.text("\n  Error: %s\n"), m.errorText(m.err))
	}
	b.WriteString(m.text("  Ctrl+L Language: English / Traditional Chinese\n"))
	return b.String()
}

func (m model) localTarget() bool {
	if m.connection.BaseURL == "" || m.localBaseURL != "" && m.connection.BaseURL == m.localBaseURL {
		return true
	}
	u, err := url.Parse(m.connection.BaseURL)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
}
