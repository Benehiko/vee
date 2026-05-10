package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Benehiko/vee/internal/monitor"
	"github.com/Benehiko/vee/internal/qemu"
	"github.com/Benehiko/vee/internal/vm"
)

// styles
var (
	styleTitle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Padding(0, 1)
	styleSelected   = lipgloss.NewStyle().Background(lipgloss.Color("237")).Bold(true)
	styleStopped    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleRunning    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleInstalling = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleReady      = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	styleErr        = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleFaint      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleHelp       = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Padding(0, 1)
	colName         = lipgloss.NewStyle().Width(18)
	colTemplate     = lipgloss.NewStyle().Width(14)
	colStatus       = lipgloss.NewStyle().Width(10)
	colPID          = lipgloss.NewStyle().Width(8)
	colCPU          = lipgloss.NewStyle().Width(8)
	colMem          = lipgloss.NewStyle().Width(10)
	colDisk         = lipgloss.NewStyle().Width(16)
	colNet          = lipgloss.NewStyle().Width(16)
	colSSH          = lipgloss.NewStyle().Width(22)
)

type listEntry struct {
	config   *vm.VMConfig
	state    *vm.VMState
	stats    monitor.Stats
	prevRaw  qemu.QMPRawCounters
	prevPoll time.Time
}

type listModel struct {
	mgr     *vm.Manager
	entries []listEntry
	cursor  int
	confirm string // non-empty when awaiting delete confirmation
	status  string // transient status message
	err     string
}

// messages
type (
	refreshMsg    []listEntry
	refreshErrMsg string
	statsMsg2     struct {
		name     string
		raw      qemu.QMPRawCounters
		polledAt time.Time
	}
	tickMsg2  time.Time
	actionErr string
)

func newListModel(mgr *vm.Manager) listModel {
	return listModel{mgr: mgr}
}

func (m listModel) Init() tea.Cmd {
	return tea.Batch(m.doRefresh(), tickList())
}

func (m listModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.KeyMsg:
		// Delete confirmation mode
		if m.confirm != "" {
			switch msg.String() {
			case "y", "Y":
				name := m.confirm
				m.confirm = ""
				return m, tea.Batch(m.doDelete(name), m.doRefresh())
			default:
				m.confirm = ""
				m.status = "Delete cancelled."
			}
			return m, nil
		}

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		case "s":
			if e := m.selected(); e != nil && !e.state.Running {
				return m, m.doStart(e.config.Name)
			}
		case "S":
			if e := m.selected(); e != nil && e.state.Running {
				return m, m.doStop(e.config.Name)
			}
		case "d":
			if e := m.selected(); e != nil {
				if e.state.Running {
					m.status = "Stop the VM before deleting."
				} else {
					m.confirm = e.config.Name
				}
			}
		case "c":
			return m, gotoCreate()
		case "m":
			if e := m.selected(); e != nil && e.state.Running && e.state.QMPSocket != "" {
				return m, gotoMonitor(e.config.Name, e.state.QMPSocket)
			} else if e != nil {
				m.status = "VM is not running."
			}
		case "r":
			return m, m.doRefresh()
		}

	case refreshMsg:
		m.entries = []listEntry(msg)
		m.err = ""
		if m.cursor >= len(m.entries) && m.cursor > 0 {
			m.cursor = len(m.entries) - 1
		}
		return m, m.doStats()

	case refreshErrMsg:
		m.err = string(msg)

	case statsMsg2:
		for i, e := range m.entries {
			if e.config.Name != msg.name {
				continue
			}
			cur := msg.raw
			prev := e.prevRaw
			var cpuPct float64
			if cur.CPUNs > 0 && !e.prevPoll.IsZero() {
				elapsed := msg.polledAt.Sub(e.prevPoll)
				if elapsed > 0 {
					deltaNs := float64(cur.CPUNs) - float64(prev.CPUNs)
					if deltaNs < 0 {
						deltaNs = float64(cur.CPUNs)
					}
					cpuPct = deltaNs / float64(elapsed.Nanoseconds())
				}
			}
			m.entries[i].stats = monitor.Stats{
				CPUPercent:     cpuPct,
				MemActual:      cur.BalloonActual,
				DiskReadBytes:  deltau(prev.DiskRdBytes, cur.DiskRdBytes),
				DiskWriteBytes: deltau(prev.DiskWrBytes, cur.DiskWrBytes),
				NetRxBytes:     deltau(prev.NetRxBytes, cur.NetRxBytes),
				NetTxBytes:     deltau(prev.NetTxBytes, cur.NetTxBytes),
			}
			m.entries[i].prevRaw = cur
			m.entries[i].prevPoll = msg.polledAt
		}

	case tickMsg2:
		return m, tea.Batch(m.doRefresh(), tickList())

	case actionErr:
		m.status = string(msg)
	}

	return m, nil
}

func (m listModel) View() string {
	var sb strings.Builder

	sb.WriteString(styleTitle.Render("  vee  "))
	sb.WriteString(styleFaint.Render("QEMU VM manager"))
	sb.WriteString("\n\n")

	// Header row
	header := colName.Render("NAME") +
		colTemplate.Render("TEMPLATE") +
		colStatus.Render("STATUS") +
		colPID.Render("PID") +
		colCPU.Render("CPU%") +
		colMem.Render("MEM") +
		colDisk.Render("DISK R/W") +
		colNet.Render("NET Rx/Tx") +
		colSSH.Render("SSH")
	sb.WriteString(styleFaint.Render(header))
	sb.WriteString("\n")

	if len(m.entries) == 0 {
		sb.WriteString(styleFaint.Render("  No VMs yet. Press c to create one."))
		sb.WriteString("\n")
	}

	for i, e := range m.entries {
		row := renderRow(e)
		if i == m.cursor {
			sb.WriteString(styleSelected.Render(row))
		} else {
			sb.WriteString(row)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\n")

	if m.confirm != "" {
		sb.WriteString(styleErr.Render(fmt.Sprintf("  Delete %q? [y/N] ", m.confirm)))
	} else if m.status != "" {
		sb.WriteString(styleRunning.Render("  " + m.status))
	} else if m.err != "" {
		sb.WriteString(styleErr.Render("  " + m.err))
	}

	sb.WriteString("\n")
	sb.WriteString(styleHelp.Render("s start  S stop  d delete  c create  m monitor  r refresh  q quit"))
	return sb.String()
}

func renderRow(e listEntry) string {
	name := colName.Render(truncate(e.config.Name, 17))
	tmpl := colTemplate.Render(truncate(e.config.Template, 13))

	var status string
	switch e.state.InstallState {
	case vm.InstallStatePending:
		status = colStatus.Render(styleInstalling.Render("installing"))
	case vm.InstallStateReady:
		if e.state.Running {
			status = colStatus.Render(styleRunning.Render("running"))
		} else {
			status = colStatus.Render(styleReady.Render("ready"))
		}
	default:
		if e.state.Running {
			status = colStatus.Render(styleRunning.Render("running"))
		} else {
			status = colStatus.Render(styleStopped.Render("stopped"))
		}
	}

	pid := "-"
	if e.state.PID > 0 {
		pid = fmt.Sprintf("%d", e.state.PID)
	}
	pidCol := colPID.Render(pid)

	cpuStr := "-"
	if e.state.Running && e.stats.CPUPercent > 0 {
		cpuStr = fmt.Sprintf("%.1f%%", e.stats.CPUPercent*100)
	}
	cpuCol := colCPU.Render(cpuStr)

	mem := colMem.Render(fmtBytes2(e.stats.MemActual))

	disk := colDisk.Render(
		fmtBytes2(e.stats.DiskReadBytes) + "/" + fmtBytes2(e.stats.DiskWriteBytes),
	)
	net := colNet.Render(
		fmtBytes2(e.stats.NetRxBytes) + "/" + fmtBytes2(e.stats.NetTxBytes),
	)

	sshVal := "-"
	if e.state.SSHPort > 0 {
		sshVal = fmt.Sprintf("127.0.0.1:%d", e.state.SSHPort)
	} else if e.config.SSHPort > 0 {
		sshVal = fmt.Sprintf("127.0.0.1:%d", e.config.SSHPort)
	}
	sshCol := colSSH.Render(sshVal)

	return name + tmpl + status + pidCol + cpuCol + mem + disk + net + sshCol
}

func (m listModel) selected() *listEntry {
	if len(m.entries) == 0 || m.cursor >= len(m.entries) {
		return nil
	}
	return &m.entries[m.cursor]
}

// async commands

func (m listModel) doRefresh() tea.Cmd {
	return func() tea.Msg {
		entries, err := m.mgr.List()
		if err != nil {
			return refreshErrMsg(err.Error())
		}
		out := make([]listEntry, len(entries))
		for i, e := range entries {
			s := e.State
			if s == nil {
				s = &vm.VMState{}
			}
			out[i] = listEntry{config: e.Config, state: s}
		}
		return refreshMsg(out)
	}
}

func (m listModel) doStats() tea.Cmd {
	var cmds []tea.Cmd
	for _, e := range m.entries {
		if !e.state.Running || e.state.QMPSocket == "" {
			continue
		}
		name := e.config.Name
		sock := e.state.QMPSocket
		cmds = append(cmds, func() tea.Msg {
			client, err := qemu.NewQMPClient(sock, 2*time.Second)
			if err != nil {
				return nil
			}
			defer func() { _ = client.Close() }()
			raw, err := client.QueryRaw()
			if err != nil {
				return nil
			}
			return statsMsg2{name: name, raw: raw, polledAt: time.Now()}
		})
	}
	return tea.Batch(cmds...)
}

func (m listModel) doStart(name string) tea.Cmd {
	return func() tea.Msg {
		if err := m.mgr.Start(context.Background(), name, false); err != nil {
			return actionErr("start: " + err.Error())
		}
		return refreshMsg(nil)
	}
}

func (m listModel) doStop(name string) tea.Cmd {
	return func() tea.Msg {
		if err := m.mgr.Stop(context.Background(), name); err != nil {
			return actionErr("stop: " + err.Error())
		}
		return refreshMsg(nil)
	}
}

func (m listModel) doDelete(name string) tea.Cmd {
	return func() tea.Msg {
		if err := m.mgr.Delete(name); err != nil {
			return actionErr("delete: " + err.Error())
		}
		return nil
	}
}

func tickList() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg2(t) })
}

// helpers

func fmtBytes2(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(b)/float64(1<<10))
	default:
		if b == 0 {
			return "-"
		}
		return fmt.Sprintf("%dB", b)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func deltau(prev, cur uint64) uint64 {
	if cur >= prev {
		return cur - prev
	}
	return cur
}
