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
	"rillway/internal/outbound"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

type Options struct {
	BaseURL        string
	Token          string
	CAFile         string
	InstallService func(context.Context) error
	InstallCommand func() *exec.Cmd
}

func Run(ctx context.Context, options Options) error {
	client, err := control.NewClient(options.BaseURL, options.Token, options.CAFile)
	if err != nil {
		return err
	}
	m := model{ctx: ctx, client: client, install: options.InstallService, installCommand: options.InstallCommand, width: 100, height: 30, loading: true, statusLoading: true, nextStatus: time.Now().Add(10 * time.Second)}
	_, err = tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	return err
}

type (
	flow      = engine.Flow
	snapshot  = engine.Snapshot
	loadedMsg struct {
		cfg      config.Config
		snapshot snapshot
		err      error
	}
)

type statusesMsg struct {
	statuses []outbound.Status
	err      error
}
type resultMsg struct {
	message string
	err     error
}
type tickMsg time.Time

type model struct {
	ctx            context.Context
	client         *control.Client
	install        func(context.Context) error
	installCommand func() *exec.Cmd
	cfg            config.Config
	statuses       []outbound.Status
	flows          []flow
	width, height  int
	page, selected int
	loading        bool
	statusLoading  bool
	nextStatus     time.Time
	ready          bool
	message        string
	err            error
	form           string
	input          string
	ruleFlow       flow
	ruleChoice     int
	ruleFamily     int
	licenseID      string
}

func (m model) Init() tea.Cmd { return tea.Batch(m.load(), m.loadStatuses(), tick()) }
func tick() tea.Cmd           { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }

func (m model) load() tea.Cmd {
	return func() tea.Msg {
		cfg, err := m.client.Config(m.ctx)
		if err != nil {
			return loadedMsg{err: err}
		}
		raw, err := m.client.Snapshot(m.ctx)
		if err != nil {
			return loadedMsg{err: err}
		}
		var snap snapshot
		err = json.Unmarshal(raw, &snap)
		return loadedMsg{cfg: cfg, snapshot: snap, err: err}
	}
}

func (m model) loadStatuses() tea.Cmd {
	return func() tea.Msg {
		statuses, err := m.client.Statuses(m.ctx)
		return statusesMsg{statuses: statuses, err: err}
	}
}

func (m model) apply(cfg config.Config) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.Apply(m.ctx, cfg)
		return resultMsg{message: "設定已儲存，只影響新連線。", err: err}
	}
}

func (m model) action(id, action, value string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.Action(m.ctx, id, action, value)
		return resultMsg{message: "出口操作已完成。", err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case loadedMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.cfg = msg.cfg
			m.flows = msg.snapshot.Flows
			m.ready = true
			m.clamp()
		}
	case statusesMsg:
		m.statusLoading = false
		if msg.err == nil {
			m.statuses = msg.statuses
		} else {
			m.err = msg.err
		}
	case resultMsg:
		m.err = msg.err
		if msg.err == nil {
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
		if !m.loading {
			m.loading = true
			commands = append(commands, m.load())
		}
		if !m.statusLoading && time.Now().After(m.nextStatus) {
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
			if m.ready {
				m.loading = true
				return m, m.load()
			}
		case "a":
			if m.ready {
				cfg := clone(m.cfg)
				cfg.Adaptive.Enabled = !cfg.Adaptive.Enabled
				return m, m.apply(cfg)
			}
		case "enter":
			if m.page == 0 && len(m.flows) > 0 {
				m.form = "rule"
				m.ruleFlow = m.flows[m.selected]
				m.ruleChoice = 0
				m.ruleFamily = 0
			}
		case "c", "d", "v", "n":
			if m.page == 1 && len(m.cfg.Outbounds) > 0 {
				actions := map[string]string{"c": "connect", "d": "disconnect", "v": "verify", "n": "register"}
				return m, m.action(m.cfg.Outbounds[m.selected].ID, actions[msg.String()], "")
			}
		case "l":
			if m.page == 1 && len(m.cfg.Outbounds) > 0 && m.cfg.Outbounds[m.selected].Type == "warp" {
				m.form = "license"
				m.licenseID = m.cfg.Outbounds[m.selected].ID
				m.input = ""
			}
		case "i":
			if m.install != nil || m.installCommand != nil {
				m.form = "install"
			}
		}
	}
	return m, nil
}

func (m model) updateForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" {
		m.form = ""
		m.input = ""
		m.licenseID = ""
		return m, nil
	}
	switch m.form {
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
			r := []rune(m.input)
			if len(r) > 0 {
				m.input = string(r[:len(r)-1])
			}
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
					m.err = fmt.Errorf("未提供服務安裝指令")
					return m, nil
				}
				return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return resultMsg{message: "背景服務已安裝。", err: err} })
			}
			return m, func() tea.Msg { return resultMsg{message: "背景服務已安裝。", err: m.install(m.ctx)} }
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
		return config.Rule{}, fmt.Errorf("這條連線沒有可用的目的地")
	}
	r := config.Rule{ID: id, Outbound: outboundID, Family: family}
	if ip, err := netip.ParseAddr(host); err == nil {
		r.CIDRs = []string{netip.PrefixFrom(ip, ip.BitLen()).String()}
	} else {
		r.Domains = []string{host}
	}
	if outboundID == "" {
		if len(candidates) == 0 {
			return config.Rule{}, fmt.Errorf("先在 Web UI 設定自適應候選出口")
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
	b.WriteString("\n  ≈ Rillway   網路分流控制台\n\n")
	for i, name := range []string{"連線總覽", "出口與 VPN", "服務設定"} {
		if m.page == i {
			fmt.Fprintf(&b, "  [%s]", name)
		} else {
			fmt.Fprintf(&b, "   %s ", name)
		}
	}
	fmt.Fprintf(&b, "\n\n  自適應：%s   設定版本：%d\n", map[bool]string{true: "啟用", false: "關閉"}[m.cfg.Adaptive.Enabled], m.cfg.Revision)
	if m.form != "" {
		b.WriteString(m.formView())
		return b.String()
	}
	if !m.ready && m.err == nil {
		b.WriteString("\n  正在連接管理服務…\n")
	} else {
		switch m.page {
		case 0:
			b.WriteString(m.flowsView())
		case 1:
			b.WriteString(m.outboundsView())
		case 2:
			fmt.Fprintf(&b, "\n  HTTP Proxy    %s\n  SOCKS5        %s\n  管理介面      %s\n  PAC           %s\n\n  Mac 的公司服務請由 PAC bypass 保留給本機 Tailscale。\n  輸入 i 可透過 CLI 安裝背景服務；Web UI 可編輯 PAC 與完整規則。\n", m.cfg.Listeners.HTTP, m.cfg.Listeners.SOCKS5, m.cfg.Listeners.Admin, m.cfg.Listeners.PAC)
		}
	}
	if m.err != nil {
		fmt.Fprintf(&b, "\n  錯誤：%s\n", m.err.Error())
	} else if m.message != "" {
		fmt.Fprintf(&b, "\n  %s\n", m.message)
	}
	b.WriteString("\n  Tab 換頁   ↑↓ 選擇   a 切換自適應   r 更新   q 離開\n")
	if m.page == 0 {
		b.WriteString("  Enter 為選定連線建立規則；既有連線保留原出口。\n")
	}
	if m.page == 1 {
		b.WriteString("  c 連線   d 斷線   v 驗證   n 註冊   l 輸入 WARP+ 授權碼\n")
	}
	return b.String()
}

func (m model) flowsView() string {
	if len(m.flows) == 0 {
		return "\n  等待第一條連線。請將瀏覽器或 Mac 指向 Rillway Proxy。\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n    %s %s %s %s\n", cell("目的地", 34), cell("出口", 15), cell("下載", 13), "上傳")
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
		ip = "上游未提供實際 IP"
	}
	status := "連線中"
	if f.Closed {
		status = "已結束"
	}
	fmt.Fprintf(&b, "\n  %s · %s · 規則 %s · 建連 %.1f ms\n  累計下載 %s / 上傳 %s\n", ip, status, f.Rule, f.ConnectMillis, humanBytes(float64(f.DownloadBytes)), humanBytes(float64(f.UploadBytes)))
	return b.String()
}

func (m model) outboundsView() string {
	if len(m.cfg.Outbounds) == 0 {
		return "\n  尚無出口，請在 Web UI 新增。\n"
	}
	var b strings.Builder
	b.WriteString("\n")
	start, end := m.visible(len(m.cfg.Outbounds))
	for i := start; i < end; i++ {
		o := m.cfg.Outbounds[i]
		state := "等待狀態"
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
		fmt.Fprintf(&b, "  %s %s %s %s\n", marker, cell(o.ID, 25), cell(o.Type, 16), state)
	}
	o := m.cfg.Outbounds[m.selected]
	for _, s := range m.statuses {
		if s.ID == o.ID {
			fmt.Fprintf(&b, "\n  %s\n", s.Detail)
			if s.Version != "" || s.Mode != "" {
				fmt.Fprintf(&b, "  版本 %s · 模式 %s · Proxy listener %t\n", s.Version, s.Mode, s.Listener)
			}
			if s.AuthURL != "" && strings.HasPrefix(s.AuthURL, "https://") {
				fmt.Fprintf(&b, "  登入：%s\n", s.AuthURL)
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
	if m.form == "license" {
		return fmt.Sprintf("\n  WARP+ 授權碼 · %s\n\n  %s▏\n\n  Enter 套用   Esc 取消\n  授權碼不會顯示或保存在 TUI。\n", m.licenseID, strings.Repeat("•", len([]rune(m.input))))
	}
	if m.form == "install" {
		return "\n  安裝 Rillway 背景服務\n\n  這會透過 CLI 建立系統服務。\n  Enter 安裝   Esc 取消\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n  為 %s 建立規則\n\n", m.ruleFlow.Host)
	for i, o := range m.cfg.Outbounds {
		marker := " "
		if m.ruleChoice == i {
			marker = "›"
		}
		fmt.Fprintf(&b, "  %s %s (%s)\n", marker, o.ID, o.Type)
	}
	marker := " "
	if m.ruleChoice == len(m.cfg.Outbounds) {
		marker = "›"
	}
	fmt.Fprintf(&b, "  %s 自適應出口\n\n  位址類型：%s\n\n  ↑↓ 選出口   f 換位址類型   Enter 儲存   Esc 取消\n", marker, []string{"雙棧自動", "IPv4", "IPv6"}[m.ruleFamily])
	if m.err != nil {
		fmt.Fprintf(&b, "\n  %s\n", m.err.Error())
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
	var b strings.Builder
	used := 0
	for _, r := range s {
		n := 1
		if r >= 0x1100 && (r <= 0x115f || r >= 0x2e80) {
			n = 2
		}
		if used+n > width-1 {
			b.WriteRune('…')
			used++
			break
		}
		b.WriteRune(r)
		used += n
	}
	if used < width {
		b.WriteString(strings.Repeat(" ", width-used))
	}
	return b.String()
}
