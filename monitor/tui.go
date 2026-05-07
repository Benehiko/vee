package monitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	styleHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleBar    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleLabel  = lipgloss.NewStyle().Width(12)
)

const barWidth = 40

type tuiModel struct {
	vmName  string
	poller  *Poller
	stats   Stats
	elapsed time.Duration
	start   time.Time
}

type (
	statsMsg Stats
	tickMsg  time.Time
)

func newTUI(vmName string, poller *Poller) tuiModel {
	return tuiModel{
		vmName: vmName,
		poller: poller,
		start:  time.Now(),
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(waitForStats(m.poller.Ch), tickEvery(time.Second))
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			m.poller.Close()
			return m, tea.Quit
		}
	case statsMsg:
		m.stats = Stats(msg)
		return m, waitForStats(m.poller.Ch)
	case tickMsg:
		m.elapsed = time.Since(m.start).Truncate(time.Second)
		return m, tickEvery(time.Second)
	}
	return m, nil
}

func (m tuiModel) View() string {
	var sb strings.Builder

	sb.WriteString(styleHeader.Render(fmt.Sprintf(" vee monitor — %s ", m.vmName)))
	sb.WriteString(styleDim.Render(fmt.Sprintf("  up %s", m.elapsed)))
	sb.WriteString("\n\n")

	sb.WriteString(styleLabel.Render("CPU"))
	cpuPct := m.stats.CPUPercent * 100
	sb.WriteString(bar(uint64(cpuPct*10), 1000))
	fmt.Fprintf(&sb, "  %.1f%%", cpuPct)
	sb.WriteString("\n\n")

	sb.WriteString(styleLabel.Render("Memory"))
	sb.WriteString(fmtBytes(m.stats.MemActual))
	sb.WriteString("\n\n")

	sb.WriteString(styleLabel.Render("Disk read"))
	sb.WriteString(bar(m.stats.DiskReadBytes, 100*1024*1024))
	sb.WriteString("  " + fmtBytesPerSec(m.stats.DiskReadBytes))
	sb.WriteString("\n")

	sb.WriteString(styleLabel.Render("Disk write"))
	sb.WriteString(bar(m.stats.DiskWriteBytes, 100*1024*1024))
	sb.WriteString("  " + fmtBytesPerSec(m.stats.DiskWriteBytes))
	sb.WriteString("\n\n")

	sb.WriteString(styleLabel.Render("Net rx"))
	sb.WriteString(bar(m.stats.NetRxBytes, 50*1024*1024))
	sb.WriteString("  " + fmtBytesPerSec(m.stats.NetRxBytes))
	sb.WriteString("\n")

	sb.WriteString(styleLabel.Render("Net tx"))
	sb.WriteString(bar(m.stats.NetTxBytes, 50*1024*1024))
	sb.WriteString("  " + fmtBytesPerSec(m.stats.NetTxBytes))
	sb.WriteString("\n\n")

	sb.WriteString(styleDim.Render("q to quit"))
	return sb.String()
}

func bar(val, max uint64) string {
	if max == 0 {
		max = 1
	}
	filled := int(float64(val) / float64(max) * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}
	empty := barWidth - filled
	return styleBar.Render("[" + strings.Repeat("█", filled) + strings.Repeat("░", empty) + "]")
}

func fmtBytes(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func fmtBytesPerSec(b uint64) string {
	return fmtBytes(b) + "/s"
}

func waitForStats(ch <-chan Stats) tea.Cmd {
	return func() tea.Msg {
		s, ok := <-ch
		if !ok {
			return tea.Quit()
		}
		return statsMsg(s)
	}
}

func tickEvery(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Run starts the TUI for a VM, blocking until the user quits.
func Run(ctx context.Context, vmName, qmpSocket string) error {
	poller, err := NewPoller(ctx, qmpSocket, time.Second)
	if err != nil {
		return fmt.Errorf("QMP connect: %w", err)
	}

	p := tea.NewProgram(newTUI(vmName, poller))
	_, err = p.Run()
	return err
}
