package tui

import (
	"fmt"
	"math"
	"rillway/internal/i18n"
	"rillway/internal/memorylimit"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"
)

func (m model) loadMemory(open bool) tea.Cmd {
	return func() tea.Msg {
		s, err := m.client.Memory(m.ctx)
		return memoryMsg{generation: m.generation, status: s, open: open, err: err}
	}
}

func (m model) memoryView() string {
	if m.memoryLoading {
		return m.text("\n  Reading service memory limits…\n")
	}
	s := m.memoryStatus
	if !s.Supported {
		if s.Reason == "" {
			return m.text("\n  Press m to read service memory limits.\n")
		}
		return "\n  " + i18n.Message(m.locale, s.Reason) + "\n"
	}
	limit := fmt.Sprintf("%.1f MiB", float64(s.LimitBytes)/float64(memorylimit.MiB))
	if s.LimitBytes == 0 {
		limit = m.text("Unlimited")
	}
	return fmt.Sprintf(m.text("\n  Memory limit: %s · current use %.1f MiB\n  Detected host: %.2f GiB · allowed %.1f–%.1f MiB\n  Minimum reason: %s\n"), limit, float64(s.CurrentBytes)/float64(memorylimit.MiB), float64(s.HostBytes)/float64(memorylimit.GiB), float64(s.MinimumBytes)/float64(memorylimit.MiB), float64(s.MaximumBytes)/float64(memorylimit.MiB), i18n.Message(m.locale, s.MinimumReason))
}

func nextMemoryUnit(mode, value string, s memorylimit.Status, previous bool) (string, string) {
	modes := []string{"percent", "MiB", "GiB"}
	index := 0
	for i, unit := range modes {
		if unit == mode {
			index = i
		}
	}
	delta := 1
	if previous {
		delta = 2
	}
	next := modes[(index+delta)%3]
	amount, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return next, value
	}
	switch mode {
	case "percent":
		amount *= float64(s.HostBytes) / 100
	case "MiB":
		amount *= float64(memorylimit.MiB)
	case "GiB":
		amount *= float64(memorylimit.GiB)
	}
	switch next {
	case "percent":
		amount = amount * 100 / float64(s.HostBytes)
	case "MiB":
		amount /= float64(memorylimit.MiB)
	case "GiB":
		amount /= float64(memorylimit.GiB)
	}
	// Round up so conversion at the minimum cannot fall below the safety floor.
	return next, strconv.FormatFloat(math.Ceil(amount*1000)/1000, 'f', 3, 64)
}
