// Package tui is an API-only terminal client; it never configures host routes.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os/exec"
	"rillway/internal/config"
	"rillway/internal/control"
	"rillway/internal/engine"
	"rillway/internal/i18n"
	"rillway/internal/outbound"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rivo/uniseg"
)

type Options struct {
	BaseURL            string
	Token              string
	CAFile             string
	Locale             i18n.Locale
	TokenFile          string
	RememberConnection func(ConnectionSettings) error
	StartCommand       func() *exec.Cmd
	InitialError       error
	InstallService     func(context.Context) error
	InstallCommand     func() *exec.Cmd
}

func Run(ctx context.Context, options Options) error {
	ctx = i18n.WithLocale(ctx, options.Locale)
	client, err := control.NewClient(options.BaseURL, options.Token, options.CAFile)
	if options.InitialError != nil {
		err = options.InitialError
	}
	m := model{ctx: ctx, locale: options.Locale, client: client, install: options.InstallService, installCommand: options.InstallCommand, width: 100, height: 30, loading: true, statusLoading: true, nextStatus: time.Now().Add(10 * time.Second), connection: ConnectionSettings{BaseURL: options.BaseURL, TokenFile: options.TokenFile, CAFile: options.CAFile}, remember: options.RememberConnection, startCommand: options.StartCommand, rememberPending: options.RememberConnection != nil}
	if options.StartCommand != nil || options.InstallCommand != nil || options.InstallService != nil {
		m.localBaseURL = options.BaseURL
	}
	if err != nil {
		m.client = nil
		m.err = err
		m.loading = false
		m.statusLoading = false
		m.openConnection()
	}
	_, err = tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	return err
}

type (
	flow      = engine.Flow
	snapshot  = engine.Snapshot
	loadedMsg struct {
		cfg        config.Config
		snapshot   snapshot
		err        error
		generation uint64
	}
)

type statusesMsg struct {
	generation uint64
	statuses   []outbound.Status
	err        error
}
type resultMsg struct {
	generation uint64
	form       string
	message    string
	err        error
}
type tickMsg time.Time

type model struct {
	connection      ConnectionSettings
	localBaseURL    string
	remember        func(ConnectionSettings) error
	rememberPending bool
	startCommand    func() *exec.Cmd
	generation      uint64
	field, outType  int
	fields          []string
	drafts          map[string][]string
	outDraft        config.Outbound
	saving          bool
	ctx             context.Context
	locale          i18n.Locale
	client          *control.Client
	install         func(context.Context) error
	installCommand  func() *exec.Cmd
	cfg             config.Config
	statuses        []outbound.Status
	flows           []flow
	width, height   int
	page, selected  int
	loading         bool
	statusLoading   bool
	nextStatus      time.Time
	ready           bool
	message         string
	err             error
	form            string
	input           string
	ruleFlow        flow
	ruleChoice      int
	ruleFamily      int
	licenseID       string
}

func (m model) Init() tea.Cmd {
	if m.client == nil {
		return tick()
	}
	return tea.Batch(m.load(), m.loadStatuses(), tick())
}

func tick() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }

func (m model) load() tea.Cmd {
	return func() tea.Msg {
		cfg, err := m.client.Config(m.ctx)
		if err != nil {
			return loadedMsg{err: err, generation: m.generation}
		}
		raw, err := m.client.Snapshot(m.ctx)
		if err != nil {
			return loadedMsg{err: err, generation: m.generation}
		}
		var snap snapshot
		err = json.Unmarshal(raw, &snap)
		return loadedMsg{cfg: cfg, snapshot: snap, err: err, generation: m.generation}
	}
}

func (m model) loadStatuses() tea.Cmd {
	return func() tea.Msg {
		statuses, err := m.client.Statuses(m.ctx)
		return statusesMsg{statuses: statuses, err: err, generation: m.generation}
	}
}

func (m model) apply(cfg config.Config) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.Apply(m.ctx, cfg)
		return resultMsg{message: "Configuration saved. Applies to new connections only.", err: err, generation: m.generation}
	}
}

func (m model) action(id, action, value string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.Action(m.ctx, id, action, value)
		return resultMsg{message: "Outbound action completed.", err: err, generation: m.generation}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case loadedMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.cfg = msg.cfg
			m.flows = msg.snapshot.Flows
			m.ready = true
			if m.rememberPending && m.remember != nil {
				m.rememberPending = false
				if err := m.remember(m.connection); err != nil {
					m.err = err
				}
			}
			m.clamp()
		} else {
			m.ready = false
		}
	case statusesMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.statusLoading = false
		if msg.err == nil {
			m.statuses = msg.statuses
		} else {
			m.err = msg.err
		}
	case resultMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.saving = false
		m.err = msg.err
		if msg.err == nil {
			if msg.form != "" && m.form == msg.form {
				m.form = ""
				m.fields = nil
			}
			m.message = msg.message
			m.loading = true
			if !m.statusLoading {
				m.statusLoading = true
				return m, tea.Batch(m.load(), m.loadStatuses())
			}
			return m, m.load()
		}
	case tickMsg:
		commands := []tea.Cmd{tick()}
		if !m.loading && m.ready && m.form == "" {
			m.loading = true
			commands = append(commands, m.load())
		}
		if m.ready && m.form == "" && !m.statusLoading && time.Now().After(m.nextStatus) {
			m.statusLoading = true
			m.nextStatus = time.Now().Add(10 * time.Second)
			commands = append(commands, m.loadStatuses())
		}
		return m, tea.Batch(commands...)
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.input = ""
			return m, tea.Quit
		}
		if msg.String() == "ctrl+l" || msg.String() == "L" && m.form != "license" && m.form != "connection" && m.form != "outbound" {
			if m.locale == i18n.TraditionalChinese {
				m.locale = i18n.English
			} else {
				m.locale = i18n.TraditionalChinese
			}
			if m.ctx == nil {
				m.ctx = context.Background()
			}
			m.ctx = i18n.WithLocale(m.ctx, m.locale)
			return m, nil
		}
		if m.form != "" {
			return m.updateForm(msg)
		}
		switch msg.String() {
		case "q", "esc":
			return m, tea.Quit
		case "tab", "right":
			m.page = (m.page + 1) % 3
			m.selected = 0
		case "shift+tab", "left":
			m.page = (m.page + 2) % 3
			m.selected = 0
		case "j", "down":
			m.selected++
			m.clamp()
		case "k", "up":
			m.selected--
			m.clamp()
		case "r":
			if m.client != nil && !m.loading {
				m.loading = true
				return m, m.load()
			}
		case "a":
			if m.ready {
				cfg := clone(m.cfg)
				cfg.Adaptive.Enabled = !cfg.Adaptive.Enabled
				return m, m.apply(cfg)
			}
		case "o":
			m.openConnection()
		case "?":
			m.form = "help"
		case "s":
			if m.startCommand != nil && m.localTarget() {
				m.form = "start"
			}
		case "+":
			if m.ready && m.page == 1 {
				m.openOutbound()
			}
		case "enter":
			if m.ready && m.page == 0 && len(m.flows) > 0 {
				m.form = "rule"
				m.ruleFlow = m.flows[m.selected]
				m.ruleChoice = 0
				selected := m.ruleFlow.Outbound
				if selected == "" {
					selected = m.cfg.DefaultOutbound
				}
				for i, o := range m.cfg.Outbounds {
					if o.ID == selected {
						m.ruleChoice = i
					}
				}
				m.ruleFamily = 0
			}
		case "c", "d", "v", "n":
			if m.ready && m.page == 1 && len(m.cfg.Outbounds) > 0 {
				actions := map[string]string{"c": "connect", "d": "disconnect", "v": "verify", "n": "register"}
				return m, m.action(m.cfg.Outbounds[m.selected].ID, actions[msg.String()], "")
			}
		case "l":
			if m.ready && m.page == 1 && len(m.cfg.Outbounds) > 0 && m.cfg.Outbounds[m.selected].Type == "warp" {
				m.form = "license"
				m.licenseID = m.cfg.Outbounds[m.selected].ID
				m.input = ""
			}
		case "i":
			if m.localTarget() && (m.install != nil || m.installCommand != nil) {
				m.form = "install"
			}
		}
	}
	return m, nil
}

func (m model) updateForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" && !m.saving {
		m.form = ""
		m.input = ""
		m.licenseID = ""
		m.fields = nil
		return m, nil
	}
	switch m.form {
	case "connection", "outbound":
		return m.updateFields(key)
	case "help":
		if key.String() == "enter" {
			m.form = ""
		}
	case "start":
		if key.String() == "enter" {
			cmd := m.startCommand()
			if cmd == nil {
				return m, nil
			}
			m.form = ""
			return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
				return resultMsg{message: "Background service started.", err: err, generation: m.generation}
			})
		}
	case "license":
		switch key.String() {
		case "enter":
			value := strings.TrimSpace(m.input)
			if value == "" {
				return m, nil
			}
			id := m.licenseID
			m.input = ""
			m.licenseID = ""
			m.form = ""
			return m, m.action(id, "license", value)
		case "backspace", "ctrl+h":
			graphemes := uniseg.NewGraphemes(m.input)
			last := 0
			for graphemes.Next() {
				last, _ = graphemes.Positions()
			}
			m.input = m.input[:last]
		default:
			if key.Type == tea.KeyRunes && len(m.input) < 1024 {
				for _, r := range key.Runes {
					if !unicode.IsControl(r) {
						m.input += string(r)
					}
				}
			}
		}
	case "install":
		if key.String() == "enter" {
			m.form = ""
			if m.installCommand != nil {
				cmd := m.installCommand()
				if cmd == nil {
					m.err = fmt.Errorf("service installation command is unavailable")
					return m, nil
				}
				return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
					return resultMsg{message: "Background service installed.", err: err, generation: m.generation}
				})
			}
			return m, func() tea.Msg {
				return resultMsg{message: "Background service installed.", err: m.install(m.ctx), generation: m.generation}
			}
		}
	case "rule":
		count := len(m.cfg.Outbounds) + 1
		switch key.String() {
		case "j", "down", "right":
			m.ruleChoice = (m.ruleChoice + 1) % count
		case "k", "up", "left":
			m.ruleChoice = (m.ruleChoice + count - 1) % count
		case "f":
			m.ruleFamily = (m.ruleFamily + 1) % 3
		case "enter":
			cfg := clone(m.cfg)
			id := fmt.Sprintf("tui-%d", time.Now().UnixNano())
			selected := ""
			if m.ruleChoice < len(cfg.Outbounds) {
				selected = cfg.Outbounds[m.ruleChoice].ID
			}
			rule, err := ruleForFlow(m.ruleFlow, id, selected, []string{"", "ipv4", "ipv6"}[m.ruleFamily], cfg.Adaptive.Candidates)
			if err != nil {
				m.err = err
				return m, nil
			}
			cfg.Rules = append([]config.Rule{rule}, cfg.Rules...)
			m.form = ""
			return m, m.apply(cfg)
		}
	}
	return m, nil
}

func clone(cfg config.Config) config.Config {
	data, _ := json.Marshal(cfg)
	var out config.Config
	_ = json.Unmarshal(data, &out)
	return out
}

func ruleForFlow(f flow, id, outboundID, family string, candidates []string) (config.Rule, error) {
	host := f.Host
	if host == "" {
		host = f.IP
	}
	if host == "" {
		return config.Rule{}, fmt.Errorf("this connection has no destination")
	}
	r := config.Rule{ID: id, Outbound: outboundID, Family: family}
	if ip, err := netip.ParseAddr(host); err == nil {
		r.CIDRs = []string{netip.PrefixFrom(ip, ip.BitLen()).String()}
	} else {
		r.Domains = []string{host}
	}
	if outboundID == "" {
		if len(candidates) == 0 {
			return config.Rule{}, fmt.Errorf("set up adaptive routing candidates in the Web UI first")
		}
		r.Adaptive = true
		r.Candidates = append([]string(nil), candidates...)
	}
	return r, nil
}

func (m *model) clamp() {
	count := len(m.flows)
	if m.page == 1 {
		count = len(m.cfg.Outbounds)
	}
	if m.page == 2 {
		count = 0
	}
	if m.selected >= count {
		m.selected = count - 1
	}
	if m.selected < 0 {
		m.selected = 0
	}
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(m.text("\n  ≈ Rillway   Routing console\n\n"))
	for i, name := range []string{"Connections", "Outbounds & VPNs", "Service settings"} {
		name = m.text(name)
		if m.page == i {
			fmt.Fprintf(&b, "  [%s]", name)
		} else {
			fmt.Fprintf(&b, "   %s ", name)
		}
	}
	if m.ready {
		if m.connection.BaseURL != "" {
			fmt.Fprintf(&b, m.text("\n  Service: %s\n"), m.connection.BaseURL)
		}
		fmt.Fprintf(&b, m.text("\n\n  Adaptive routing: %s   Configuration revision: %d\n"), m.text(map[bool]string{true: "Enabled", false: "Disabled"}[m.cfg.Adaptive.Enabled]), m.cfg.Revision)
	}
	if m.form != "" {
		b.WriteString(m.formView())
		return b.String()
	}
	if !m.ready {
		if m.err == nil {
			b.WriteString(m.text("\n  Connecting to the management service…\n"))
		} else {
			fmt.Fprintf(&b, m.text("\n  Service unavailable: %s\n  No configuration has been loaded.\n  Press o to choose the running service on your VM or this machine.\n"), m.connection.BaseURL)
			if m.startCommand != nil && m.localTarget() {
				b.WriteString(m.text("  Press s to start an installed local service, or i to install one.\n"))
			}
		}
	} else {
		switch m.page {
		case 0:
			b.WriteString(m.flowsView())
		case 1:
			b.WriteString(m.outboundsView())
		case 2:
			fmt.Fprintf(&b, m.text("\n  HTTP proxy    %s\n  SOCKS5        %s\n  Management UI %s\n  PAC           %s\n\n  Use PAC bypass for company services on Mac to keep using local Tailscale.\n  Edit PAC and all routing rules in the Web UI.\n"), m.cfg.Listeners.HTTP, m.cfg.Listeners.SOCKS5, m.cfg.Listeners.Admin, m.cfg.Listeners.PAC)
			if m.localTarget() && (m.installCommand != nil || m.install != nil) {
				b.WriteString(m.text("  Press i to install the background service.\n"))
			} else {
				b.WriteString(m.text("  Start or install the service on the machine running it.\n"))
			}
		}
	}
	if m.err != nil {
		fmt.Fprintf(&b, m.text("\n  Error: %s\n"), m.errorText(m.err))
	} else if m.message != "" {
		fmt.Fprintf(&b, "\n  %s\n", m.text(m.message))
	}
	b.WriteString(m.text("\n  Tab Switch tab   ↑↓ Select   a Toggle adaptive routing   r Refresh   q Quit\n"))
	b.WriteString(m.languageHelp())
	b.WriteString(m.text("  o Service connection   ? How to use\n"))
	if m.ready && m.page == 0 {
		b.WriteString(m.text("  Enter Create routing rule; existing connections keep their outbound.\n"))
	}
	if m.ready && m.page == 1 {
		b.WriteString(m.text("  + Add outbound with suggested values\n"))
		b.WriteString(m.text("  c Connect   d Disconnect   v Verify   n Register   l WARP+ license key\n"))
	}
	return b.String()
}

func (m model) flowsView() string {
	if len(m.flows) == 0 {
		return m.text("\n  No connections yet. Point your browser or Mac at the Rillway proxy.\n")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n    %s %s %s %s\n", cell(m.text("Destination"), 34), cell(m.text("Outbound"), 15), cell(m.text("Download"), 13), m.text("Upload"))
	start, end := m.visible(len(m.flows))
	for i := start; i < end; i++ {
		f := m.flows[i]
		marker := " "
		if i == m.selected {
			marker = "›"
		}
		host := f.Host
		if host == "" {
			host = f.IP
		}
		fmt.Fprintf(&b, "  %s %s %s %s %s\n", marker, cell(host, 34), cell(f.Outbound, 15), cell(humanBytes(f.DownloadRate)+"/s", 13), humanBytes(f.UploadRate)+"/s")
	}
	f := m.flows[m.selected]
	ip := f.IP
	if ip == "" {
		ip = m.text("IP not reported by upstream")
	}
	status := "Active"
	if f.Closed {
		status = "Closed"
	}
	fmt.Fprintf(&b, m.text("\n  %s · %s · Routing rule %s · Connect %.1f ms\n  Downloaded %s / Uploaded %s\n"), ip, m.text(status), f.Rule, f.ConnectMillis, humanBytes(float64(f.DownloadBytes)), humanBytes(float64(f.UploadBytes)))
	return b.String()
}

func (m model) outboundsView() string {
	if len(m.cfg.Outbounds) == 0 {
		return m.text("\n  No outbounds configured. Add one in the Web UI.\n")
	}
	var b strings.Builder
	b.WriteString("\n")
	start, end := m.visible(len(m.cfg.Outbounds))
	for i := start; i < end; i++ {
		o := m.cfg.Outbounds[i]
		state := "Waiting for status"
		for _, s := range m.statuses {
			if s.ID == o.ID {
				state = s.State
			}
		}
		if !o.Enabled {
			state = "disabled"
		}
		marker := " "
		if i == m.selected {
			marker = "›"
		}
		fmt.Fprintf(&b, "  %s %s %s %s\n", marker, cell(o.ID, 25), cell(m.text(o.Type), 16), m.text(state))
	}
	o := m.cfg.Outbounds[m.selected]
	for _, s := range m.statuses {
		if s.ID == o.ID {
			fmt.Fprintf(&b, "\n  %s\n", i18n.Message(m.locale, s.Detail))
			if s.Version != "" || s.Mode != "" {
				fmt.Fprintf(&b, m.text("  Version %s · Mode %s · Proxy listener %t\n"), s.Version, s.Mode, s.Listener)
			}
			if s.AuthURL != "" && strings.HasPrefix(s.AuthURL, "https://") {
				fmt.Fprintf(&b, m.text("  Sign in: %s\n"), s.AuthURL)
			}
		}
	}
	return b.String()
}

func (m model) visible(count int) (int, int) {
	limit := m.height - 17
	if limit < 3 {
		limit = 3
	}
	start := 0
	if m.selected >= limit {
		start = m.selected - limit + 1
	}
	end := start + limit
	if end > count {
		end = count
	}
	return start, end
}

func (m model) formView() string {
	if m.form == "connection" || m.form == "outbound" {
		return m.fieldsView()
	}
	if m.form == "start" {
		return m.text("\n  Start the installed local background service?\n  Enter Start   Esc Cancel\n") + m.languageHelp()
	}
	if m.form == "help" {
		if !m.ready {
			return m.text("\n  First connect to your Rillway service\n  Close this page and press o to choose its HTTPS address, token file and certificate.\n  Once connected, press ? again to see your actual proxy addresses.\n  Enter / Esc Close   Ctrl+L Language\n")
		}
		return fmt.Sprintf(m.text("\n  How to use Rillway\n\n  1. Press o to connect to a running service. A VM uses its own HTTPS address.\n  2. Tab to Outbounds. Press + to add; common values are filled in.\n     Select WARP, then n Register, c Connect and v Verify.\n  3. Set your browser HTTP proxy to %s, or use the PAC URL below.\n     http://%s/proxy.pac\n  4. Connections shows only traffic sent through this proxy.\n     Select a connection and press Enter to choose its future route.\n\n  Company services should bypass Rillway on your Mac.\n  Enter / Esc Close   Ctrl+L Language\n"), m.cfg.Listeners.HTTP, m.cfg.Listeners.PAC)
	}

	if m.form == "license" {
		return fmt.Sprintf(m.text("\n  WARP+ license key · %s\n\n  %s▏\n\n  Enter Apply   Esc Cancel\n  The key is masked and is not saved by this TUI.\n"), m.licenseID, strings.Repeat("•", uniseg.GraphemeClusterCount(m.input))) + m.languageHelp()
	}
	if m.form == "install" {
		return m.text("\n  Install the Rillway background service\n\n  This creates a system service.\n  Enter Install   Esc Cancel\n") + m.languageHelp()
	}
	var b strings.Builder
	fmt.Fprintf(&b, m.text("\n  Create routing rule for %s\n\n"), m.ruleFlow.Host)
	for i, o := range m.cfg.Outbounds {
		marker := " "
		if m.ruleChoice == i {
			marker = "›"
		}
		fmt.Fprintf(&b, "  %s %s (%s)\n", marker, o.ID, m.text(o.Type))
	}
	marker := " "
	if m.ruleChoice == len(m.cfg.Outbounds) {
		marker = "›"
	}
	fmt.Fprintf(&b, m.text("  %s Adaptive routing\n\n  IP version: %s\n\n  ↑↓ Select outbound   f Change IP version   Enter Save   Esc Cancel\n"), marker, m.text([]string{"Automatic (dual stack)", "IPv4", "IPv6"}[m.ruleFamily]))
	b.WriteString(m.languageHelp())
	if m.err != nil {
		fmt.Fprintf(&b, "\n  %s\n", m.errorText(m.err))
	}
	return b.String()
}

func humanBytes(n float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", n, units[i])
	}
	return fmt.Sprintf("%.1f %s", n, units[i])
}

func cell(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if used := uniseg.StringWidth(s); used <= width {
		return s + strings.Repeat(" ", width-used)
	}
	var b strings.Builder
	used := 0
	graphemes := uniseg.NewGraphemes(s)
	for graphemes.Next() {
		n := graphemes.Width()
		if used+n > width-1 {
			break
		}
		b.WriteString(graphemes.Str())
		used += n
	}
	b.WriteRune('…')
	used++
	if used < width {
		b.WriteString(strings.Repeat(" ", width-used))
	}
	return b.String()
}
