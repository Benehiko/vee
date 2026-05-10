package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

// editField enumerates every editable field in VMConfig.
type editField int

const (
	efMemory editField = iota
	efCPUs
	efSockets
	efCores
	efThreads
	efCPUModel
	efNICMode
	efNICBridge
	efNICMAC
	efGPUMode
	efGPUPCI
	efGPUAntiDetect
	efVGA
	efHeadless
	efUEFI
	efSPICEPort
	efSSHPort
	efHostname
	efCount
)

var editFieldLabels = [efCount]string{
	efMemory:        "Memory",
	efCPUs:          "CPUs",
	efSockets:       "Sockets",
	efCores:         "Cores",
	efThreads:       "Threads",
	efCPUModel:      "CPU model",
	efNICMode:       "NIC mode",
	efNICBridge:     "NIC bridge",
	efNICMAC:        "NIC MAC",
	efGPUMode:       "GPU mode",
	efGPUPCI:        "GPU PCI addr",
	efGPUAntiDetect: "Anti-detect",
	efVGA:           "VGA",
	efHeadless:      "Headless",
	efUEFI:          "UEFI",
	efSPICEPort:     "SPICE port",
	efSSHPort:       "SSH port",
	efHostname:      "Hostname",
}

// cycleFields are fields whose value cycles through a fixed set (←/→).
var cycleOptions = map[editField][]string{
	efNICMode:       {"user", "bridge"},
	efGPUMode:       {"none", "virtio", "passthrough"},
	efGPUAntiDetect: {"false", "true"},
	efHeadless:      {"false", "true"},
	efUEFI:          {"false", "true"},
}

type editModel struct {
	prov       provider.Provider
	cfg        *vm.VMConfig
	field      editField
	inputs     [efCount]textinput.Model
	err        string
	saved      bool
	standalone bool // true when run outside the main app (tea.Quit instead of gotoList)
}

type editSavedMsg struct{ err error }

var (
	styleEditTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Padding(0, 1)
	styleEditLabel = lipgloss.NewStyle().Width(16).Foreground(lipgloss.Color("7"))
	styleEditVal   = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	styleEditFocus = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	styleEditHelp  = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Padding(0, 1)
	styleEditErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleEditCycle = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
)

func cfgToInputValues(cfg *vm.VMConfig) [efCount]string {
	spicePort := ""
	if cfg.SPICE != nil {
		spicePort = strconv.Itoa(cfg.SPICE.Port)
	}
	return [efCount]string{
		efMemory:        cfg.Memory,
		efCPUs:          strconv.Itoa(cfg.CPUs),
		efSockets:       strconv.Itoa(cfg.Sockets),
		efCores:         strconv.Itoa(cfg.Cores),
		efThreads:       strconv.Itoa(cfg.Threads),
		efCPUModel:      cfg.CPUModel,
		efNICMode:       cfg.NIC.Mode,
		efNICBridge:     cfg.NIC.Bridge,
		efNICMAC:        cfg.NIC.MAC,
		efGPUMode:       string(cfg.GPU.Mode),
		efGPUPCI:        cfg.GPU.PCIAddr,
		efGPUAntiDetect: boolStr(cfg.GPU.AntiDetect),
		efVGA:           cfg.VGA,
		efHeadless:      boolStr(cfg.Headless),
		efUEFI:          boolStr(cfg.UEFI.Enabled),
		efSPICEPort:     spicePort,
		efSSHPort:       strconv.Itoa(cfg.SSHPort),
		efHostname:      cfg.Hostname,
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func newEditModel(p provider.Provider, cfg *vm.VMConfig) editModel {
	vals := cfgToInputValues(cfg)
	var inputs [efCount]textinput.Model
	for i := range efCount {
		ti := textinput.New()
		ti.SetValue(vals[editField(i)])
		if _, isCycle := cycleOptions[editField(i)]; isCycle {
			ti.Prompt = ""
		}
		inputs[i] = ti
	}
	inputs[0].Focus()
	return editModel{
		prov:   p,
		cfg:    cfg,
		field:  0,
		inputs: inputs,
	}
}

func (m editModel) Init() tea.Cmd { return textinput.Blink }

func (m editModel) done() tea.Cmd {
	if m.standalone {
		return tea.Quit
	}
	return gotoList()
}

func (m editModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case editSavedMsg:
		if msg.err != nil {
			m.err = msg.err.Error()
		} else {
			m.saved = true
		}
		return m, m.done()

	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return m, m.done()
		case "tab", "down":
			m.inputs[m.field].Blur()
			m.field = (m.field + 1) % efCount
			m.inputs[m.field].Focus()
			return m, nil
		case "shift+tab", "up":
			m.inputs[m.field].Blur()
			m.field = (m.field + efCount - 1) % efCount
			m.inputs[m.field].Focus()
			return m, nil
		case "left":
			if opts, ok := cycleOptions[m.field]; ok {
				cur := m.inputs[m.field].Value()
				m.inputs[m.field].SetValue(cyclePrev(opts, cur))
				return m, nil
			}
		case "right":
			if opts, ok := cycleOptions[m.field]; ok {
				cur := m.inputs[m.field].Value()
				m.inputs[m.field].SetValue(cycleNext(opts, cur))
				return m, nil
			}
		case "enter":
			return m, m.doSave()
		}
	}

	var cmd tea.Cmd
	m.inputs[m.field], cmd = m.inputs[m.field].Update(msg)
	return m, cmd
}

func cycleNext(opts []string, cur string) string {
	for i, o := range opts {
		if o == cur {
			return opts[(i+1)%len(opts)]
		}
	}
	return opts[0]
}

func cyclePrev(opts []string, cur string) string {
	for i, o := range opts {
		if o == cur {
			return opts[(i+len(opts)-1)%len(opts)]
		}
	}
	return opts[len(opts)-1]
}

func (m editModel) View() string {
	var sb strings.Builder
	sb.WriteString(styleEditTitle.Render("  Edit VM: "+m.cfg.Name+"  ") + "\n\n")

	for i := range efCount {
		f := editField(i)
		label := styleEditLabel.Render(editFieldLabels[f])
		_, isCycle := cycleOptions[f]
		focused := m.field == f

		var val string
		if isCycle {
			val = renderCycle(cycleOptions[f], m.inputs[i].Value(), focused)
		} else {
			raw := m.inputs[i].Value()
			if focused {
				raw += "█"
				val = styleEditFocus.Render(raw)
			} else {
				val = styleEditVal.Render(raw)
			}
		}
		sb.WriteString("  " + label + val + "\n")
	}

	sb.WriteString("\n")
	if m.err != "" {
		sb.WriteString(styleEditErr.Render("  Error: "+m.err) + "\n\n")
	}
	sb.WriteString(styleEditHelp.Render("tab/↑↓ navigate  ←/→ cycle options  enter save  esc cancel"))
	return sb.String()
}

func renderCycle(opts []string, cur string, focused bool) string {
	var parts []string
	for _, o := range opts {
		if o == cur {
			if focused {
				parts = append(parts, styleEditFocus.Render("[ "+o+" ]"))
			} else {
				parts = append(parts, styleEditCycle.Render("[ "+o+" ]"))
			}
		} else {
			parts = append(parts, styleFaint.Render(o))
		}
	}
	return strings.Join(parts, " ")
}

func (m editModel) doSave() tea.Cmd {
	cfg := m.cfg
	p := m.prov
	inputs := m.inputs

	return func() tea.Msg {
		atoi := func(s string) int {
			v, _ := strconv.Atoi(strings.TrimSpace(s))
			return v
		}
		parseBool := func(s string) bool { return strings.TrimSpace(s) == "true" }

		cfg.Memory = strings.TrimSpace(inputs[efMemory].Value())
		cfg.CPUs = atoi(inputs[efCPUs].Value())
		cfg.Sockets = atoi(inputs[efSockets].Value())
		cfg.Cores = atoi(inputs[efCores].Value())
		cfg.Threads = atoi(inputs[efThreads].Value())
		cfg.CPUModel = strings.TrimSpace(inputs[efCPUModel].Value())
		cfg.NIC.Mode = strings.TrimSpace(inputs[efNICMode].Value())
		cfg.NIC.Bridge = strings.TrimSpace(inputs[efNICBridge].Value())
		cfg.NIC.MAC = strings.TrimSpace(inputs[efNICMAC].Value())
		cfg.GPU.Mode = vm.GPUMode(strings.TrimSpace(inputs[efGPUMode].Value()))
		cfg.GPU.PCIAddr = strings.TrimSpace(inputs[efGPUPCI].Value())
		cfg.GPU.AntiDetect = parseBool(inputs[efGPUAntiDetect].Value())
		cfg.VGA = strings.TrimSpace(inputs[efVGA].Value())
		cfg.Headless = parseBool(inputs[efHeadless].Value())
		cfg.UEFI.Enabled = parseBool(inputs[efUEFI].Value())
		cfg.Hostname = strings.TrimSpace(inputs[efHostname].Value())

		if port := atoi(inputs[efSPICEPort].Value()); port > 0 {
			if cfg.SPICE == nil {
				cfg.SPICE = &vm.SPICEConfig{DisableTicketing: true}
			}
			cfg.SPICE.Port = port
		} else {
			cfg.SPICE = nil
		}

		if port := atoi(inputs[efSSHPort].Value()); port > 0 {
			cfg.SSHPort = port
		} else {
			cfg.SSHPort = 0
		}

		if err := vm.SaveConfig(p.Config().StoragePath, cfg); err != nil {
			return editSavedMsg{err: err}
		}
		return editSavedMsg{}
	}
}

// RunConfigEditor loads an existing VM config and opens the edit TUI.
// If name is empty it launches the list screen first (user picks from there).
func RunConfigEditor(ctx context.Context, p provider.Provider, name string) error {
	if name != "" {
		cfg, err := vm.LoadConfig(p.Config().StoragePath, name)
		if err != nil {
			return fmt.Errorf("load config %q: %w", name, err)
		}
		m := newEditModel(p, cfg)
		m.standalone = true
		prog := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
		_, err = prog.Run()
		return err
	}
	// No name — open the normal TUI; user can navigate and edit from there.
	return Run(ctx, p)
}
