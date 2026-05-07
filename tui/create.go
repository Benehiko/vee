package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/templates"
	"github.com/Benehiko/vee/vm"
)

var (
	styleFormTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Padding(0, 1)
	styleFieldLabel = lipgloss.NewStyle().Width(14).Foreground(lipgloss.Color("7"))
	styleFieldValue = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	styleFieldFocus = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	styleFormHelp   = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Padding(0, 1)
	styleFormErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

var templateNames = []string{
	"ubuntu-server",
	"devbox",
	"server",
	"torrent",
	"gaming",
	"windows",
}

// createField identifies which form field is focused.
type createField int

const (
	fieldName createField = iota
	fieldTemplate
	fieldMemory
	fieldCPUs
	fieldCount
)

type createModel struct {
	mgr        *vm.Manager
	prov       provider.Provider
	field      createField
	name       string
	tmplIdx    int
	memory     string
	cpus       string
	err        string
	submitting bool
}

type createDoneMsg struct{ err error }

func newCreateModel(mgr *vm.Manager, p provider.Provider) createModel {
	return createModel{
		mgr:    mgr,
		prov:   p,
		memory: "2G",
		cpus:   "2",
	}
}

func (m createModel) Init() tea.Cmd { return nil }

func (m createModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.KeyMsg:
		if m.submitting {
			return m, nil
		}
		switch msg.String() {
		case "esc":
			return m, gotoList()
		case "tab", "down":
			m.field = (m.field + 1) % fieldCount
		case "shift+tab", "up":
			m.field = (m.field - 1 + fieldCount) % fieldCount
		case "enter":
			if m.field == fieldCount-1 {
				m.submitting = true
				return m, m.doSubmit()
			}
			m.field = (m.field + 1) % fieldCount
		case "backspace":
			switch m.field {
			case fieldName:
				if len(m.name) > 0 {
					m.name = m.name[:len(m.name)-1]
				}
			case fieldMemory:
				if len(m.memory) > 0 {
					m.memory = m.memory[:len(m.memory)-1]
				}
			case fieldCPUs:
				if len(m.cpus) > 0 {
					m.cpus = m.cpus[:len(m.cpus)-1]
				}
			}
		case "left":
			if m.field == fieldTemplate && m.tmplIdx > 0 {
				m.tmplIdx--
			}
		case "right":
			if m.field == fieldTemplate && m.tmplIdx < len(templateNames)-1 {
				m.tmplIdx++
			}
		default:
			ch := msg.String()
			if len(ch) == 1 {
				switch m.field {
				case fieldName:
					m.name += ch
				case fieldMemory:
					m.memory += ch
				case fieldCPUs:
					m.cpus += ch
				}
			}
		}

	case createDoneMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
			m.submitting = false
			return m, nil
		}
		return m, gotoList()
	}

	return m, nil
}

func (m createModel) View() string {
	var sb strings.Builder

	sb.WriteString(styleFormTitle.Render("  Create VM  "))
	sb.WriteString("\n\n")

	fields := []struct {
		label string
		value string
		f     createField
	}{
		{"Name", m.name + cursor(m.field == fieldName), fieldName},
		{"Template", templateSelector(m.tmplIdx, m.field == fieldTemplate), fieldTemplate},
		{"Memory", m.memory + cursor(m.field == fieldMemory), fieldMemory},
		{"CPUs", m.cpus + cursor(m.field == fieldCPUs), fieldCPUs},
	}

	for _, f := range fields {
		label := styleFieldLabel.Render(f.label)
		var val string
		if m.field == f.f {
			val = styleFieldFocus.Render(f.value)
		} else {
			val = styleFieldValue.Render(f.value)
		}
		sb.WriteString("  " + label + val + "\n")
	}

	sb.WriteString("\n")
	if m.err != "" {
		sb.WriteString(styleFormErr.Render("  Error: "+m.err) + "\n\n")
	}
	if m.submitting {
		sb.WriteString(styleFieldFocus.Render("  Creating…") + "\n")
	}

	sb.WriteString(styleFormHelp.Render("tab/↑↓ navigate  ←/→ choose template  enter submit  esc cancel"))
	return sb.String()
}

func templateSelector(idx int, focused bool) string {
	var parts []string
	for i, t := range templateNames {
		if i == idx {
			if focused {
				parts = append(parts, styleFieldFocus.Render("[ "+t+" ]"))
			} else {
				parts = append(parts, styleFieldValue.Render("[ "+t+" ]"))
			}
		} else {
			parts = append(parts, styleFaint.Render(t))
		}
	}
	return strings.Join(parts, " ")
}

func cursor(focused bool) string {
	if focused {
		return "█"
	}
	return ""
}

func (m createModel) doSubmit() tea.Cmd {
	name := m.name
	tmpl := templateNames[m.tmplIdx]
	memory := m.memory
	cpus := m.cpus

	mgr := m.mgr
	prov := m.prov

	return func() tea.Msg {
		cfg, err := buildConfig(context.Background(), prov, mgr, name, tmpl, memory, cpus)
		if err != nil {
			return createDoneMsg{err: err}
		}
		if err := mgr.Create(context.Background(), cfg); err != nil {
			return createDoneMsg{err: err}
		}
		return createDoneMsg{}
	}
}

func buildConfig(ctx context.Context, p provider.Provider, mgr *vm.Manager, name, tmpl, memory, cpusStr string) (*vm.VMConfig, error) {
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}

	var cpus int
	if _, err := fmt.Sscan(cpusStr, &cpus); err != nil || cpus < 1 {
		cpus = 2
	}

	var cfg *vm.VMConfig
	switch tmpl {
	case "gaming":
		cfg = templates.NewGamingConfig(p, name, "", "")
	case "torrent":
		cfg = templates.NewTorrentConfig(p, name, 0)
	case "devbox":
		cfg = templates.NewDevboxConfig(p, name, nil)
	case "server":
		cfg = templates.NewServerConfig(p, name, nil)
	case "windows":
		cfg = templates.NewWindowsConfig(p, name)
	default:
		conf := p.Config()
		cfg = &vm.VMConfig{
			Name:     name,
			Template: tmpl,
			Memory:   memory,
			CPUs:     cpus,
			Sockets:  1,
			Cores:    cpus,
			Threads:  1,
			CPUModel: conf.DefaultCPUModel,
			NIC: vm.NICConfig{
				Mode:  "user",
				Model: "virtio-net-pci",
			},
			GPU:  vm.GPUConfig{Mode: vm.GPUNone},
			UEFI: vm.UEFIConfig{Enabled: true},
		}
	}

	if memory != "" {
		cfg.Memory = memory
	}
	if cpus > 0 {
		cfg.CPUs = cpus
		cfg.Cores = cpus
	}

	_ = ctx
	_ = mgr
	return cfg, nil
}
