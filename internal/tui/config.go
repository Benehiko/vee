package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Benehiko/vee/internal/blockdev"
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
	efCPUPinning
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
	efCPUPinning:    "CPU pin",
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

// diskFocus tracks which part of the disk section is focused.
type diskFocus int

const (
	dfNone    diskFocus = iota // focus is in the fixed fields above
	dfDisk                     // focus is on an existing disk row
	dfAddMode                  // focus is on the "add disk" mode selector
	dfAddSize                  // focus is on the new-disk size input
	dfAddDev                   // focus is on the new-disk device selector
)

type editModel struct {
	prov       provider.Provider
	cfg        *vm.VMConfig
	field      editField
	inputs     [efCount]textinput.Model
	err        string
	saved      bool
	standalone bool // true when run outside the main app (tea.Quit instead of gotoList)

	// disk section
	df          diskFocus
	diskIdx     int      // selected existing disk row
	addMode     diskMode // mode for the "add" row
	addSize     string   // new disk size input
	blockDevs   []blockdev.Device
	blockDevIdx int
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
	pinParts := make([]string, len(cfg.CPUPinning))
	for i, c := range cfg.CPUPinning {
		pinParts[i] = strconv.Itoa(c)
	}
	pinStr := strings.Join(pinParts, ",")
	return [efCount]string{
		efMemory:        cfg.Memory,
		efCPUs:          strconv.Itoa(cfg.CPUs),
		efSockets:       strconv.Itoa(cfg.Sockets),
		efCores:         strconv.Itoa(cfg.Cores),
		efThreads:       strconv.Itoa(cfg.Threads),
		efCPUModel:      cfg.CPUModel,
		efCPUPinning:    pinStr,
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

// parseCPUPinning converts a comma-separated string like "4,5,6,7" to []int.
// Invalid or empty entries are silently dropped.
func parseCPUPinning(s string) []int {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if n, err := strconv.Atoi(part); err == nil && n >= 0 {
			out = append(out, n)
		}
	}
	return out
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
	devs, _ := blockdev.ListUnmounted()
	return editModel{
		prov:      p,
		cfg:       cfg,
		field:     0,
		inputs:    inputs,
		df:        dfNone,
		addMode:   diskModeNew,
		addSize:   "20G",
		blockDevs: devs,
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
			return m.focusNext(), nil

		case "shift+tab", "up":
			return m.focusPrev(), nil

		case "left":
			switch m.df {
			case dfNone:
				if opts, ok := cycleOptions[m.field]; ok {
					cur := m.inputs[m.field].Value()
					m.inputs[m.field].SetValue(cyclePrev(opts, cur))
					return m, nil
				}
			case dfAddMode:
				if m.addMode > diskModeNew {
					m.addMode--
				}
				return m, nil
			case dfAddDev:
				if m.blockDevIdx > 0 {
					m.blockDevIdx--
				}
				return m, nil
			}

		case "right":
			switch m.df {
			case dfNone:
				if opts, ok := cycleOptions[m.field]; ok {
					cur := m.inputs[m.field].Value()
					m.inputs[m.field].SetValue(cycleNext(opts, cur))
					return m, nil
				}
			case dfAddMode:
				if int(m.addMode) < len(diskModeNames)-1 {
					m.addMode++
				}
				return m, nil
			case dfAddDev:
				if m.blockDevIdx < len(m.blockDevs)-1 {
					m.blockDevIdx++
				}
				return m, nil
			}

		case "d", "x":
			// Delete focused existing disk.
			if m.df == dfDisk && len(m.cfg.Disks) > 0 {
				m.cfg.Disks = append(m.cfg.Disks[:m.diskIdx], m.cfg.Disks[m.diskIdx+1:]...)
				if m.diskIdx >= len(m.cfg.Disks) && m.diskIdx > 0 {
					m.diskIdx--
				}
				if len(m.cfg.Disks) == 0 {
					m.df = dfAddMode
				}
				return m, nil
			}

		case "enter":
			switch m.df {
			case dfNone:
				return m, m.doSave()
			case dfDisk:
				// enter on existing disk row → move to add section
				m.df = dfAddMode
				return m, nil
			case dfAddMode:
				if m.addMode == diskModeNone {
					// "none" in add context means cancel / go back
					m.df = dfNone
					m.field = efCount - 1
					m.inputs[m.field].Focus()
					return m, nil
				}
				// advance to size or dev
				if m.addMode == diskModeNew {
					m.df = dfAddSize
				} else {
					m.df = dfAddDev
				}
				return m, nil
			case dfAddSize:
				m.commitAddDisk()
				m.df = dfAddMode
				return m, nil
			case dfAddDev:
				m.commitAddDisk()
				m.df = dfAddMode
				return m, nil
			}

		case "backspace":
			if m.df == dfAddSize {
				if len(m.addSize) > 0 {
					m.addSize = m.addSize[:len(m.addSize)-1]
				}
				return m, nil
			}

		default:
			if m.df == dfAddSize {
				ch := msg.String()
				if len(ch) == 1 {
					m.addSize += ch
				}
				return m, nil
			}
		}
	}

	if m.df == dfNone {
		var cmd tea.Cmd
		m.inputs[m.field], cmd = m.inputs[m.field].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *editModel) commitAddDisk() {
	switch m.addMode {
	case diskModeNew:
		if m.addSize != "" {
			m.cfg.Disks = append(m.cfg.Disks, vm.DiskConfig{
				Size:      m.addSize,
				Format:    "qcow2",
				Interface: "virtio",
				Media:     "disk",
			})
		}
	case diskModePassthrough:
		if len(m.blockDevs) > 0 {
			dev := m.blockDevs[m.blockDevIdx]
			m.cfg.Disks = append(m.cfg.Disks, vm.DiskConfig{
				Path:        dev.ByIDPath,
				Passthrough: true,
			})
		}
	}
	m.addSize = "20G"
	m.blockDevIdx = 0
}

func (m editModel) focusNext() editModel {
	switch m.df {
	case dfNone:
		m.inputs[m.field].Blur()
		if int(m.field) < int(efCount)-1 {
			m.field++
			m.inputs[m.field].Focus()
		} else {
			// move into disk section
			if len(m.cfg.Disks) > 0 {
				m.df = dfDisk
				m.diskIdx = 0
			} else {
				m.df = dfAddMode
			}
		}
	case dfDisk:
		if m.diskIdx < len(m.cfg.Disks)-1 {
			m.diskIdx++
		} else {
			m.df = dfAddMode
		}
	case dfAddMode:
		switch m.addMode {
		case diskModeNew:
			m.df = dfAddSize
		case diskModePassthrough:
			m.df = dfAddDev
		default:
			// wrap back to fixed fields
			m.df = dfNone
			m.field = 0
			m.inputs[m.field].Focus()
		}
	case dfAddSize, dfAddDev:
		// wrap back to fixed fields
		m.df = dfNone
		m.field = 0
		m.inputs[m.field].Focus()
	}
	return m
}

func (m editModel) focusPrev() editModel {
	switch m.df {
	case dfNone:
		m.inputs[m.field].Blur()
		if m.field > 0 {
			m.field--
			m.inputs[m.field].Focus()
		} else {
			// wrap to end of disk section
			m.df = dfAddMode
		}
	case dfDisk:
		if m.diskIdx > 0 {
			m.diskIdx--
		} else {
			// back to fixed fields
			m.df = dfNone
			m.field = efCount - 1
			m.inputs[m.field].Focus()
		}
	case dfAddMode:
		if len(m.cfg.Disks) > 0 {
			m.df = dfDisk
			m.diskIdx = len(m.cfg.Disks) - 1
		} else {
			m.df = dfNone
			m.field = efCount - 1
			m.inputs[m.field].Focus()
		}
	case dfAddSize, dfAddDev:
		m.df = dfAddMode
	}
	return m
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
		focused := m.df == dfNone && m.field == f

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

	// Disk section
	sb.WriteString("\n")
	sb.WriteString(styleEditLabel.Render("  Disks") + "\n")

	for i, d := range m.cfg.Disks {
		focused := m.df == dfDisk && m.diskIdx == i
		label := diskRowLabel(d)
		var row string
		if focused {
			row = styleEditFocus.Render("▶ "+label) + styleEditHelp.Render("  d=delete")
		} else {
			row = styleEditVal.Render("  " + label)
		}
		sb.WriteString("    " + row + "\n")
	}
	if len(m.cfg.Disks) == 0 {
		sb.WriteString("    " + styleFaint.Render("(none)") + "\n")
	}

	// Add disk row
	sb.WriteString("\n  " + styleEditLabel.Render("Add disk") + diskAddView(m) + "\n")

	sb.WriteString("\n")
	if m.err != "" {
		sb.WriteString(styleEditErr.Render("  Error: "+m.err) + "\n\n")
	}
	var helpLine string
	switch m.df {
	case dfDisk:
		helpLine = "tab/↑↓ navigate  d/x delete disk  enter next  esc cancel"
	case dfAddMode, dfAddSize, dfAddDev:
		helpLine = "tab/↑↓ navigate  ←/→ choose  enter confirm  esc cancel"
	default:
		helpLine = "tab/↑↓ navigate  ←/→ cycle options  enter save  esc cancel"
	}
	sb.WriteString(styleEditHelp.Render(helpLine))
	return sb.String()
}

func diskRowLabel(d vm.DiskConfig) string {
	if d.Passthrough {
		return "passthrough: " + d.Path
	}
	s := d.Size
	if d.Interface != "" {
		s += " " + d.Interface
	}
	if d.Path != "" {
		s += " " + d.Path
	}
	return s
}

func diskAddView(m editModel) string {
	switch m.df {
	case dfAddMode:
		return diskModeSelector(m.addMode, true)
	case dfAddSize:
		return diskModeSelector(m.addMode, false) + "  size: " + styleEditFocus.Render(m.addSize+"█")
	case dfAddDev:
		return diskModeSelector(m.addMode, false) + "  " + diskDevSelector(m.blockDevs, m.blockDevIdx, true)
	default:
		return diskModeSelector(m.addMode, false)
	}
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
		cfg.CPUPinning = parseCPUPinning(inputs[efCPUPinning].Value())
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

		// cfg.Disks is mutated in-place during the TUI session.

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
