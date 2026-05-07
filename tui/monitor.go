package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Benehiko/vee/monitor"
)

var (
	styleMonHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Padding(0, 1)
	styleMonBar    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleMonDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleMonLabel  = lipgloss.NewStyle().Width(14)
)

const monBarWidth = 40

type monitorModel struct {
	vmName    string
	qmpSocket string
	poller    *monitor.Poller
	stats     monitor.Stats
	elapsed   time.Duration
	start     time.Time
	err       string
}

type (
	monStatsMsg monitor.Stats
	monTickMsg  time.Time
	monErrMsg   string
)

func newMonitorModel(vmName, qmpSocket string) monitorModel {
	return monitorModel{
		vmName:    vmName,
		qmpSocket: qmpSocket,
		start:     time.Now(),
	}
}

func (m monitorModel) Init() tea.Cmd {
	return func() tea.Msg {
		poller, err := monitor.NewPoller(context.Background(), m.qmpSocket, time.Second)
		if err != nil {
			return monErrMsg(err.Error())
		}
		// Store the poller — send it back as a message so Update can assign it.
		return monPollerReady{poller: poller}
	}
}

type monPollerReady struct{ poller *monitor.Poller }

func (m monitorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			if m.poller != nil {
				m.poller.Close()
			}
			return m, gotoList()
		}

	case monPollerReady:
		m.poller = msg.poller
		return m, tea.Batch(monWaitStats(m.poller.Ch), monTick())

	case monStatsMsg:
		m.stats = monitor.Stats(msg)
		return m, monWaitStats(m.poller.Ch)

	case monTickMsg:
		m.elapsed = time.Since(m.start).Truncate(time.Second)
		return m, monTick()

	case monErrMsg:
		m.err = string(msg)
	}
	return m, nil
}

func (m monitorModel) View() string {
	var sb strings.Builder

	sb.WriteString(styleMonHeader.Render(fmt.Sprintf("  vee monitor — %s  ", m.vmName)))
	sb.WriteString(styleMonDim.Render(fmt.Sprintf("up %s", m.elapsed)))
	sb.WriteString("\n\n")

	if m.err != "" {
		sb.WriteString(styleErr.Render("Error: " + m.err))
		sb.WriteString("\n\n")
		sb.WriteString(styleMonDim.Render("esc/q to go back"))
		return sb.String()
	}

	sb.WriteString(styleMonLabel.Render("Memory"))
	sb.WriteString(fmtBytes2(m.stats.MemActual))
	sb.WriteString("\n\n")

	sb.WriteString(styleMonLabel.Render("Disk read"))
	sb.WriteString(monBar(m.stats.DiskReadBytes, 100*1024*1024))
	sb.WriteString("  " + fmtBytes2(m.stats.DiskReadBytes) + "/s")
	sb.WriteString("\n")

	sb.WriteString(styleMonLabel.Render("Disk write"))
	sb.WriteString(monBar(m.stats.DiskWriteBytes, 100*1024*1024))
	sb.WriteString("  " + fmtBytes2(m.stats.DiskWriteBytes) + "/s")
	sb.WriteString("\n\n")

	sb.WriteString(styleMonLabel.Render("Net rx"))
	sb.WriteString(monBar(m.stats.NetRxBytes, 50*1024*1024))
	sb.WriteString("  " + fmtBytes2(m.stats.NetRxBytes) + "/s")
	sb.WriteString("\n")

	sb.WriteString(styleMonLabel.Render("Net tx"))
	sb.WriteString(monBar(m.stats.NetTxBytes, 50*1024*1024))
	sb.WriteString("  " + fmtBytes2(m.stats.NetTxBytes) + "/s")
	sb.WriteString("\n\n")

	sb.WriteString(styleMonDim.Render("esc/q to go back"))
	return sb.String()
}

func monBar(val, max uint64) string {
	if max == 0 {
		max = 1
	}
	filled := int(float64(val) / float64(max) * float64(monBarWidth))
	if filled > monBarWidth {
		filled = monBarWidth
	}
	empty := monBarWidth - filled
	return styleMonBar.Render("[" + strings.Repeat("█", filled) + strings.Repeat("░", empty) + "]")
}

func monWaitStats(ch <-chan monitor.Stats) tea.Cmd {
	return func() tea.Msg {
		s, ok := <-ch
		if !ok {
			return gotoList()
		}
		return monStatsMsg(s)
	}
}

func monTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return monTickMsg(t) })
}
