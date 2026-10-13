// Package tui is an API-only terminal client; it never configures host routes.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"rillway/internal/access"
	"rillway/internal/config"
	"rillway/internal/control"
	"rillway/internal/engine"
	"rillway/internal/i18n"
	"rillway/internal/outbound"
	"rillway/internal/resourcecontrol"
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
	m := model{ctx: ctx, locale: options.Locale, client: client, install: options.InstallService, installCommand: options.InstallCommand, width: 100, height: 30, loading: true, statusLoading: true, outboundRefresh: 30 * time.Second, refreshConfigured: true, nextStatus: time.Now().Add(30 * time.Second), connection: ConnectionSettings{BaseURL: options.BaseURL, TokenFile: options.TokenFile, CAFile: options.CAFile}, managementToken: strings.TrimSpace(options.Token), remember: options.RememberConnection, startCommand: options.StartCommand, rememberPending: options.RememberConnection != nil}
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
		info       control.Info
		snapshot   snapshot
		err        error
		generation uint64
	}
)

type statusesMsg struct {
	generation uint64
	sequence   uint64
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

type sourceSnapshotMsg struct {
	generation uint64
	snapshot   access.SourceSnapshot
	err        error
}

type blockSourceMsg struct {
	generation uint64
	result     control.BlockSourceResult
	address    string
	err        error
}
type memoryMsg struct {
	generation uint64
	status     resourcecontrol.MemoryStatus
	open       bool
	err        error
}

type warpLogMsg struct {
	generation uint64
	status     resourcecontrol.WarpLogStatus
	open       bool
	err        error
}

type warpLogResultMsg struct {
	generation uint64
	status     resourcecontrol.WarpLogStatus
	err        error
}

type model struct {
	connection        ConnectionSettings
	managementToken   string
	tokenVisible      bool
	advancedVisible   bool
	serverInfo        control.Info
	memoryStatus      resourcecontrol.MemoryStatus
	memoryLoading     bool
	warpLogStatus     resourcecontrol.WarpLogStatus
	warpLogLoading    bool
	outboundRefresh   time.Duration
	refreshConfigured bool
	localBaseURL      string
	remember          func(ConnectionSettings) error
	rememberPending   bool
	startCommand      func() *exec.Cmd
	generation        uint64
	statusSequence    uint64
	field, outType    int
	fields            []string
	drafts            map[string][]string
	outDraft          config.Outbound
	editingSourceRule string
	saving            bool
	ctx               context.Context
	locale            i18n.Locale
	client            *control.Client
	install           func(context.Context) error
	installCommand    func() *exec.Cmd
	cfg               config.Config
	statuses          []outbound.Status
	flows             []flow
	sourceSnapshot    access.SourceSnapshot
	sourceViewRules   bool
	targetClient      access.SourceClient
	targetRule        config.SourceAccessRule
	width, height     int
	page, selected    int
	loading           bool
	statusLoading     bool
	sourceLoading     bool
	nextStatus        time.Time
	nextSource        time.Time
	ready             bool
	message           string
	messageArgs       []any
	err               error
	form              string
	input             string
	ruleFlow          flow
	ruleChoice        int
	ruleFamily        int
	licenseID         string
}

func (m model) Init() tea.Cmd {
	if m.client == nil {
		return tick()
	}
	return tea.Batch(m.load(), m.loadStatuses(false, m.statusSequence), m.loadSources(), tick())
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
		if err != nil {
			return loadedMsg{err: err, generation: m.generation}
		}
		info, err := m.client.Info(m.ctx)
		var apiError *control.APIError
		if errors.As(err, &apiError) && apiError.Status == 404 {
			info, err = control.Info{}, nil // Older daemons do not expose a version.
		}
		return loadedMsg{cfg: cfg, info: info, snapshot: snap, err: err, generation: m.generation}
	}
}

func (m model) loadStatuses(force bool, sequence uint64) tea.Cmd {
	return func() tea.Msg {
		statuses, err := m.client.Statuses(m.ctx, force)
		return statusesMsg{statuses: statuses, err: err, generation: m.generation, sequence: sequence}
	}
}

func (m model) loadSources() tea.Cmd {
	return func() tea.Msg {
		snap, err := m.client.SourceClients(m.ctx)
		return sourceSnapshotMsg{snapshot: snap, err: err, generation: m.generation}
	}
}

func (m model) blockSource(address string) tea.Cmd {
	return func() tea.Msg {
		res, err := m.client.BlockSource(m.ctx, m.cfg.Revision, address)
		return blockSourceMsg{result: res, address: address, err: err, generation: m.generation}
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
	case memoryMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.memoryLoading = false
		if m.form == "memory" {
			return m, nil
		}
		m.memoryStatus = msg.status
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		if msg.open {
			if !msg.status.Supported {
				m.err = errors.New(msg.status.Reason)
				return m, nil
			}
			m.form, m.field, m.err = "memory", 0, nil
			m.fields = []string{msg.status.Mode, msg.status.Value}
		}
	case warpLogMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.warpLogLoading = false
		if m.form == "warp_log" {
			if msg.err == nil {
				m.warpLogStatus = msg.status
			}
			return m, nil
		}
		m.warpLogStatus = msg.status
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		if msg.open {
			if !msg.status.Supported {
				reason := msg.status.Reason
				if reason == "" {
					reason = "WARP service logging is unsupported on this host."
				}
				m.err = errors.New(reason)
				return m, nil
			}
			m.form, m.field, m.err = "warp_log", 0, nil
			initialLevel := string(msg.status.Level)
			if initialLevel == "" {
				initialLevel = "info"
			}
			m.fields = []string{initialLevel}
		}
	case warpLogResultMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.saving = false
		if msg.err != nil {
			var apiErr *control.APIError
			isConflict := errors.Is(msg.err, resourcecontrol.ErrWarpLogConflict) ||
				(errors.As(msg.err, &apiErr) && apiErr.Status == 409) ||
				strings.Contains(strings.ToLower(msg.err.Error()), "conflict") ||
				strings.Contains(strings.ToLower(msg.err.Error()), "changed elsewhere")
			if isConflict {
				m.err = errors.New("WARP logging settings changed elsewhere. Revision refreshed; press Enter to retry")
				m.warpLogLoading = true
				return m, m.loadWarpLog(false)
			}
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.form = ""
		m.fields = nil
		m.warpLogStatus = msg.status
		m.message = "WARP log level saved. WARP service was restarted."
		m.messageArgs = nil
		m.warpLogLoading = true
		return m, m.loadWarpLog(false)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case loadedMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.loading = false
		// A refresh already in flight must not replace the revision or choices behind
		// an open editor or confirmation dialog.
		if m.form == "rule" || m.form == "outbound" || m.form == "source_rule" || m.form == "block_source" || m.form == "delete_source_rule" || m.form == "memory" || m.form == "warp_log" {
			return m, nil
		}
		m.err = msg.err
		if msg.err == nil {
			var selectedFlow uint64
			var selectedOutbound string
			if m.page == 0 && m.selected < len(m.flows) {
				selectedFlow = m.flows[m.selected].ID
			}
			if m.page == 1 && m.selected < len(m.cfg.Outbounds) {
				selectedOutbound = m.cfg.Outbounds[m.selected].ID
			}
			var selectedRuleID string
			if m.page == 2 && m.sourceViewRules && m.selected < len(m.cfg.SourceAccess.Rules) {
				selectedRuleID = m.cfg.SourceAccess.Rules[m.selected].ID
			}
			var selectedClientAddr string
			if m.page == 2 && !m.sourceViewRules && m.selected < len(m.sourceSnapshot.Clients) {
				selectedClientAddr = m.sourceSnapshot.Clients[m.selected].Address
			}
			m.cfg = msg.cfg
			m.serverInfo = msg.info
			m.flows = msg.snapshot.Flows
			if selectedFlow != 0 {
				for i, f := range m.flows {
					if f.ID == selectedFlow {
						m.selected = i
						break
					}
				}
			}
			if selectedOutbound != "" {
				for i, o := range m.cfg.Outbounds {
					if o.ID == selectedOutbound {
						m.selected = i
						break
					}
				}
			}
			if selectedRuleID != "" {
				for i, r := range m.cfg.SourceAccess.Rules {
					if r.ID == selectedRuleID {
						m.selected = i
						break
					}
				}
			}
			if selectedClientAddr != "" {
				for i, c := range m.sourceSnapshot.Clients {
					if c.Address == selectedClientAddr {
						m.selected = i
						break
					}
				}
			}
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
		if msg.generation != m.generation || msg.sequence != m.statusSequence {
			return m, nil
		}
		m.statusLoading = false
		if msg.err == nil {
			m.statuses = msg.statuses
		} else {
			m.err = msg.err
		}
	case sourceSnapshotMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.sourceLoading = false
		if m.form == "source_rule" || m.form == "block_source" || m.form == "delete_source_rule" {
			return m, nil
		}
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		var selectedClientAddr string
		if m.page == 2 && !m.sourceViewRules && m.selected < len(m.sourceSnapshot.Clients) {
			selectedClientAddr = m.sourceSnapshot.Clients[m.selected].Address
		}
		m.sourceSnapshot = msg.snapshot
		if selectedClientAddr != "" {
			for i, c := range m.sourceSnapshot.Clients {
				if c.Address == selectedClientAddr {
					m.selected = i
					break
				}
			}
		}
		m.clamp()
	case blockSourceMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.saving = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.form = ""
		m.err = nil
		m.cfg = msg.result.Config
		m.message = "Source client blocked (%d connection(s) disconnected)."
		m.messageArgs = []any{msg.result.Disconnected}
		m.sourceLoading = true
		m.loading = true
		return m, tea.Batch(m.load(), m.loadSources())
	case resultMsg:
		if msg.generation != m.generation {
			return m, nil
		}
		m.saving = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		if msg.form != "" && m.form == msg.form {
			m.form = ""
			m.fields = nil
			m.editingSourceRule = ""
		}
		m.message = msg.message
		m.messageArgs = nil
		if msg.form == "memory" {
			m.memoryLoading = true
			return m, m.loadMemory(false)
		}
		if msg.form == "source_rule" || msg.form == "delete_source_rule" {
			m.sourceLoading = true
			m.loading = true
			return m, tea.Batch(m.load(), m.loadSources())
		}
		m.loading = true
		if !m.statusLoading {
			m.statusLoading = true
			m.statusSequence++
			return m, tea.Batch(m.load(), m.loadStatuses(false, m.statusSequence))
		}
		return m, m.load()
	case tickMsg:
		commands := []tea.Cmd{tick()}
		interval := m.outboundRefresh
		if !m.refreshConfigured && interval == 0 {
			interval = 30 * time.Second
		}
		now := time.Now()
		statusDue := m.ready && m.page == 1 && m.form == "" && !m.loading && !m.statusLoading && interval > 0 && now.After(m.nextStatus)
		if !m.loading && m.ready && m.form == "" && !statusDue {
			m.loading = true
			commands = append(commands, m.load())
		}
		if statusDue {
			m.statusLoading = true
			m.nextStatus = now.Add(interval)
			m.statusSequence++
			commands = append(commands, m.loadStatuses(false, m.statusSequence))
		}
		if m.ready && m.page == 2 && m.form == "" && !m.sourceLoading && now.After(m.nextSource) {
			m.sourceLoading = true
			m.nextSource = now.Add(5 * time.Second)
			commands = append(commands, m.loadSources())
		}
		return m, tea.Batch(commands...)
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.input = ""
			return m, tea.Quit
		}
		if msg.String() == "ctrl+l" || msg.String() == "L" && m.form != "license" && m.form != "connection" && m.form != "outbound" && m.form != "source_rule" && m.form != "warp_log" {
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
			prevPage := m.page
			m.page = (m.page + 1) % 4
			m.selected = 0
			m.tokenVisible = false
			m.advancedVisible = false
			if m.page == 1 && prevPage != 1 {
				interval := m.outboundRefresh
				if !m.refreshConfigured && interval == 0 {
					interval = 30 * time.Second
				}
				if interval > 0 {
					m.nextStatus = time.Now().Add(interval)
				}
			}
		case "shift+tab", "left":
			prevPage := m.page
			m.page = (m.page + 3) % 4
			m.selected = 0
			m.tokenVisible = false
			m.advancedVisible = false
			if m.page == 1 && prevPage != 1 {
				interval := m.outboundRefresh
				if !m.refreshConfigured && interval == 0 {
					interval = 30 * time.Second
				}
				if interval > 0 {
					m.nextStatus = time.Now().Add(interval)
				}
			}
		case "j", "down":
			m.selected++
			m.clamp()
		case "k", "up":
			m.selected--
			m.clamp()
		case "r":
			if m.client != nil && !m.loading && (m.page != 1 || !m.statusLoading) {
				m.loading = true
				if m.page == 1 {
					m.statusLoading = true
					interval := m.outboundRefresh
					if !m.refreshConfigured && interval == 0 {
						interval = 30 * time.Second
					}
					if interval > 0 {
						m.nextStatus = time.Now().Add(interval)
					}
					m.statusSequence++
					return m, tea.Batch(m.load(), m.loadStatuses(true, m.statusSequence))
				}
				if m.page == 3 {
					var cmds []tea.Cmd
					cmds = append(cmds, m.load())
					if !m.memoryLoading {
						m.memoryLoading = true
						cmds = append(cmds, m.loadMemory(false))
					}
					if !m.warpLogLoading {
						m.warpLogLoading = true
						cmds = append(cmds, m.loadWarpLog(false))
					}
					return m, tea.Batch(cmds...)
				}
				if m.page == 2 {
					m.sourceLoading = true
					return m, tea.Batch(m.load(), m.loadSources())
				}
				return m, m.load()
			}
		case "p":
			if m.ready && m.page == 1 {
				m.cycleRefresh()
			}
		case "a":
			if m.ready {
				cfg := clone(m.cfg)
				cfg.Adaptive.Enabled = !cfg.Adaptive.Enabled
				return m, m.apply(cfg)
			}
		case "o":
			m.openConnection()
		case "t":
			if m.ready && m.page == 3 && m.managementToken != "" {
				m.tokenVisible = !m.tokenVisible
			}
		case "x":
			if m.ready && m.page == 3 {
				m.advancedVisible = !m.advancedVisible
			}
		case "m":
			if m.ready && m.page == 3 && !m.memoryLoading {
				m.memoryLoading = true
				return m, m.loadMemory(true)
			}
		case "?":
			m.form = "help"
		case "s":
			if m.startCommand != nil && m.localTarget() {
				m.form = "start"
			}
		case "+":
			if m.ready && m.page == 1 {
				m.openOutbound()
			} else if m.ready && m.page == 2 {
				m.openAddSourceRule()
			}
		case "e":
			if m.ready && m.page == 2 && m.sourceViewRules && len(m.cfg.SourceAccess.Rules) > 0 {
				m.openEditSourceRule(m.cfg.SourceAccess.Rules[m.selected])
			}
		case " ":
			if m.ready && m.page == 2 && m.sourceViewRules && len(m.cfg.SourceAccess.Rules) > 0 {
				cfg := clone(m.cfg)
				cfg.SourceAccess.Rules[m.selected].Enabled = !cfg.SourceAccess.Rules[m.selected].Enabled
				return m, m.apply(cfg)
			}
		case "v":
			if m.ready && m.page == 1 && len(m.cfg.Outbounds) > 0 {
				return m, m.action(m.cfg.Outbounds[m.selected].ID, "verify", "")
			} else if m.ready && m.page == 2 {
				m.sourceViewRules = !m.sourceViewRules
				m.selected = 0
				m.clamp()
			}
		case "b":
			if m.ready && m.page == 2 && !m.sourceViewRules && len(m.sourceSnapshot.Clients) > 0 {
				m.form = "block_source"
				m.targetClient = m.sourceSnapshot.Clients[m.selected]
			}
		case "d":
			if m.ready && m.page == 1 && len(m.cfg.Outbounds) > 0 {
				return m, m.action(m.cfg.Outbounds[m.selected].ID, "disconnect", "")
			} else if m.ready && m.page == 2 && m.sourceViewRules && len(m.cfg.SourceAccess.Rules) > 0 {
				m.form = "delete_source_rule"
				m.targetRule = m.cfg.SourceAccess.Rules[m.selected]
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
		case "c", "n":
			if m.ready && m.page == 1 && len(m.cfg.Outbounds) > 0 {
				actions := map[string]string{"c": "connect", "n": "register"}
				return m, m.action(m.cfg.Outbounds[m.selected].ID, actions[msg.String()], "")
			}
		case "l":
			if m.ready && m.page == 1 && len(m.cfg.Outbounds) > 0 && m.cfg.Outbounds[m.selected].Type == "warp" {
				m.form = "license"
				m.licenseID = m.cfg.Outbounds[m.selected].ID
				m.input = ""
			} else if m.ready && m.page == 3 && !m.warpLogLoading {
				m.warpLogLoading = true
				return m, m.loadWarpLog(true)
			}
		case "i":
			if m.localTarget() && (m.install != nil || m.installCommand != nil) {
				m.form = "install"
			}
		}
		if m.ready && m.page == 3 && (msg.String() == "tab" || msg.String() == "right" || msg.String() == "shift+tab" || msg.String() == "left") {
			var cmds []tea.Cmd
			if !m.memoryLoading {
				m.memoryLoading = true
				cmds = append(cmds, m.loadMemory(false))
			}
			if !m.warpLogLoading {
				m.warpLogLoading = true
				cmds = append(cmds, m.loadWarpLog(false))
			}
			if len(cmds) > 0 {
				return m, tea.Batch(cmds...)
			}
		}
		if m.ready && m.page == 2 && (msg.String() == "tab" || msg.String() == "right" || msg.String() == "shift+tab" || msg.String() == "left") && !m.sourceLoading {
			m.sourceLoading = true
			return m, m.loadSources()
		}
	}
	return m, nil
}

func (m model) updateForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" && !m.saving {
		m.form = ""
		m.input = ""
		m.licenseID = ""
		m.editingSourceRule = ""
		m.fields = nil
		return m, nil
	}
	switch m.form {
	case "connection", "outbound", "memory", "source_rule", "warp_log":
		return m.updateFields(key)
	case "block_source":
		if key.String() == "enter" {
			m.saving = true
			return m, m.blockSource(m.targetClient.Address)
		}
	case "delete_source_rule":
		if key.String() == "enter" {
			cfg := clone(m.cfg)
			var filtered []config.SourceAccessRule
			for _, r := range cfg.SourceAccess.Rules {
				if r.ID != m.targetRule.ID {
					filtered = append(filtered, r)
				}
			}
			cfg.SourceAccess.Rules = filtered
			m.saving = true
			return m, func() tea.Msg {
				_, err := m.client.Apply(m.ctx, cfg)
				return resultMsg{message: "Source access rule deleted.", err: err, generation: m.generation, form: "delete_source_rule"}
			}
		}
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
		if m.sourceViewRules {
			count = len(m.cfg.SourceAccess.Rules)
		} else {
			count = len(m.sourceSnapshot.Clients)
		}
	}
	if m.page == 3 {
		count = 0
	}
	if m.selected >= count {
		m.selected = count - 1
	}
	if m.selected < 0 {
		m.selected = 0
	}
}

func (m *model) openAddSourceRule() {
	m.form = "source_rule"
	m.field = 0
	m.editingSourceRule = ""
	m.fields = []string{"", "", "allow", "", "true", ""}
	m.err = nil
}

func (m *model) openEditSourceRule(r config.SourceAccessRule) {
	m.form = "source_rule"
	m.field = 0
	m.editingSourceRule = r.ID
	m.fields = []string{r.ID, r.Name, r.Action, strings.Join(r.CIDRs, ", "), fmt.Sprint(r.Enabled), r.Note}
	m.err = nil
}

func (m model) sourceVisible(count int) (int, int) {
	if m.width >= 90 {
		return m.visible(count)
	}
	limit := max(1, (m.height-17)/3)
	start := max(0, m.selected-limit+1)
	return start, min(count, start+limit)
}

func (m model) sourcesView() string {
	var b strings.Builder
	if !m.sourceViewRules {
		fmt.Fprintf(&b, "\n  %s\n\n", m.text("Clients"))
		if len(m.sourceSnapshot.Clients) == 0 {
			b.WriteString(m.text("  No client activity observed.\n"))
			return b.String()
		}
		if m.width >= 90 {
			fmt.Fprintf(&b, "    %s %s %s %s %s\n", cell(m.text("Address"), 20), cell(m.text("Status"), 10), cell(m.text("Active conn"), 14), cell(m.text("Total conn"), 14), m.text("Last seen"))
		}
		start, end := m.sourceVisible(len(m.sourceSnapshot.Clients))
		for i := start; i < end; i++ {
			c := m.sourceSnapshot.Clients[i]
			marker := " "
			if i == m.selected {
				marker = "›"
			}
			status := m.text("Allowed")
			if c.Decision == "deny" {
				status = m.text("Denied")
			}
			tot := fmt.Sprintf("%d / %d", c.AcceptedConnections, c.DeniedConnections)
			seen := c.LastSeen.Format("15:04:05")
			if m.width < 90 {
				fmt.Fprintf(&b, "  %s %s\n    %s\n    %s\n", marker, cell(c.Address, max(0, m.width-4)), cell(fmt.Sprintf("%s · %d %s", status, c.ActiveConnections, m.text("Active conn")), max(0, m.width-4)), cell(tot+" · "+seen, max(0, m.width-4)))
			} else {
				fmt.Fprintf(&b, "  %s %s %s %s %s %s\n", marker, cell(c.Address, 20), cell(status, 10), cell(fmt.Sprint(c.ActiveConnections), 14), cell(tot, 14), seen)
			}
		}
	} else {
		fmt.Fprintf(&b, "\n  %s\n\n", m.text("Rules"))
		if len(m.cfg.SourceAccess.Rules) == 0 {
			b.WriteString(m.text("  No source access rules configured.\n"))
			return b.String()
		}
		if m.width >= 90 {
			fmt.Fprintf(&b, "    %s\n", cell(m.text("Name / ID")+" · "+m.text("Action")+" · "+m.text("Status")+" · "+m.text("IP / CIDR list"), max(0, m.width-4)))
		}
		start, end := m.sourceVisible(len(m.cfg.SourceAccess.Rules))
		for i := start; i < end; i++ {
			r := m.cfg.SourceAccess.Rules[i]
			marker := " "
			if i == m.selected {
				marker = "›"
			}
			name := r.Name
			if name == "" {
				name = r.ID
			}
			action := m.text("Allowed")
			if r.Action == "deny" {
				action = m.text("Denied")
			}
			status := m.text("Enabled")
			if !r.Enabled {
				status = m.text("Disabled")
			}
			cidrs := strings.Join(r.CIDRs, ", ")
			if m.width < 90 {
				fmt.Fprintf(&b, "  %s %s\n    %s\n    %s\n", marker, cell(name, max(0, m.width-4)), cell(action+" · "+status, max(0, m.width-4)), cell(cidrs, max(0, m.width-4)))
			} else {
				fmt.Fprintf(&b, "  %s %s %s %s %s\n", marker, cell(name, 24), cell(action, 10), cell(status, 12), cell(cidrs, max(0, m.width-53)))
			}
		}
	}
	return b.String()
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(m.text("\n  ≈ Rillway   Routing console\n\n"))
	for i, name := range []string{"Connections", "Outbounds & VPNs", "Source access", "Service settings"} {
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
		if m.serverInfo.Version == "" {
			b.WriteString(m.text("\n  Program version unavailable\n"))
		} else {
			fmt.Fprintf(&b, m.text("\n  Program version: %s\n"), m.serverInfo.Version)
		}
		fmt.Fprintf(&b, m.text("  Adaptive routing: %s\n"), m.text(map[bool]string{true: "Enabled", false: "Disabled"}[m.cfg.Adaptive.Enabled]))
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
			b.WriteString(m.sourcesView())
		case 3:
			fmt.Fprintf(&b, m.text("\n  HTTP proxy    %s\n  SOCKS5        %s\n  Management UI %s\n  PAC           %s\n\n  Use PAC bypass for company services on Mac to keep using local Tailscale.\n  Edit PAC and all routing rules in the Web UI.\n"), m.cfg.Listeners.HTTP, m.cfg.Listeners.SOCKS5, m.cfg.Listeners.Admin, m.cfg.Listeners.PAC)
			b.WriteString(m.managementTokenView())
			b.WriteString(m.memoryView())
			b.WriteString(m.warpLogView())
			if m.advancedVisible {
				fmt.Fprintf(&b, m.text("\n  Advanced information\n  Configuration revision: %d\n  Tracks settings changes and prevents conflicting edits. Separate from the program version.\n"), m.cfg.Revision)
			}
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
		if len(m.messageArgs) > 0 {
			fmt.Fprintf(&b, "\n  %s\n", i18n.Format(m.locale, catalog, m.message, m.messageArgs...))
		} else {
			fmt.Fprintf(&b, "\n  %s\n", m.text(m.message))
		}
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
		fmt.Fprintf(&b, m.text("  p Auto refresh: %s   r Refresh now\n"), m.refreshLabel())
	}
	if m.ready && m.page == 2 {
		b.WriteString(m.text("  v Toggle Clients / Rules view\n"))
		if !m.sourceViewRules {
			b.WriteString(m.text("  b Block selected client\n"))
		} else {
			b.WriteString(m.text("  + Add rule   e Edit rule   Space Toggle rule   d Delete rule\n"))
		}
	}
	if m.ready && m.page == 3 {
		b.WriteString(m.text("  m Edit service memory limit (percent / MiB / GiB)\n"))
		b.WriteString(m.text("  l Edit WARP service log level\n"))
		b.WriteString(m.text("  t Show / hide Web UI token\n"))
		b.WriteString(m.text("  x Show / hide advanced information\n"))
	}
	return b.String()
}

func (m model) managementTokenView() string {
	if m.managementToken == "" {
		return m.text("\n  Web UI token  Unavailable in this session. Press o to load a token file.\n")
	}
	if !m.tokenVisible {
		return m.text("\n  Web UI token  Hidden (press t to show)\n")
	}
	return fmt.Sprintf(m.text("\n  Web UI token  %s\n  Anyone with this token can manage Rillway. Press t to hide it.\n"), m.managementToken)
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
	fmt.Fprintf(&b, m.text("\n  Auto refresh: %s\n\n"), m.refreshLabel())
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
	groupLimit := max(0, m.height-17-(end-start))
	width := m.width
	if width <= 0 {
		width = 120
	}
	for _, group := range m.cfg.OutboundGroups[:min(len(m.cfg.OutboundGroups), groupLimit)] {
		members := strings.Join(group.Members, ", ")
		if group.Type == "select" {
			members += " · " + m.text("selected") + " " + group.Selected
		}
		fmt.Fprintf(&b, m.text("\n  Group %s · %s · %s\n"), strings.TrimSpace(cell(group.ID, 25)), strings.TrimSpace(cell(m.text(group.Type), 16)), strings.TrimSpace(cell(members, max(0, width-50))))
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
	if m.form == "connection" || m.form == "outbound" || m.form == "memory" || m.form == "source_rule" || m.form == "warp_log" {
		return m.fieldsView()
	}
	if m.form == "block_source" {
		cidr := m.targetClient.Address + "/32"
		if ip, err := netip.ParseAddr(m.targetClient.Address); err == nil {
			cidr = netip.PrefixFrom(ip, ip.BitLen()).String()
		}
		res := fmt.Sprintf(m.text("\n  Block source client %s (%s)?\n  This will disconnect %d active connection(s) immediately.\n  Enter Block and disconnect   Esc Cancel\n"), m.targetClient.Address, cidr, m.targetClient.ActiveConnections) + m.languageHelp()
		if m.err != nil {
			res += fmt.Sprintf(m.text("\n  Error: %s\n"), m.errorText(m.err))
		}
		return res
	}
	if m.form == "delete_source_rule" {
		res := fmt.Sprintf(m.text("\n  Delete source rule %s (%s)?\n  Enter Delete   Esc Cancel\n"), m.targetRule.Name, m.targetRule.ID) + m.languageHelp()
		if m.err != nil {
			res += fmt.Sprintf(m.text("\n  Error: %s\n"), m.errorText(m.err))
		}
		return res
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

var refreshIntervals = []time.Duration{
	0,
	10 * time.Second,
	30 * time.Second,
	time.Minute,
	5 * time.Minute,
}

func (m *model) initRefresh() {
	if !m.refreshConfigured && m.outboundRefresh == 0 {
		m.outboundRefresh = 30 * time.Second
	}
}

func (m *model) cycleRefresh() {
	m.initRefresh()
	var current int
	for i, d := range refreshIntervals {
		if d == m.outboundRefresh {
			current = i
			break
		}
	}
	m.outboundRefresh = refreshIntervals[(current+1)%len(refreshIntervals)]
	m.refreshConfigured = true
	if m.outboundRefresh > 0 {
		m.nextStatus = time.Now().Add(m.outboundRefresh)
	} else {
		m.nextStatus = time.Time{}
	}
}

func (m model) refreshLabel() string {
	interval := m.outboundRefresh
	if !m.refreshConfigured && interval == 0 {
		interval = 30 * time.Second
	}
	switch interval {
	case 0:
		return m.text("Off")
	case 10 * time.Second:
		return m.text("10s")
	case 30 * time.Second:
		return m.text("30s")
	case time.Minute:
		return m.text("1m")
	case 5 * time.Minute:
		return m.text("5m")
	default:
		return fmt.Sprintf("%ds", int(interval.Seconds()))
	}
}
