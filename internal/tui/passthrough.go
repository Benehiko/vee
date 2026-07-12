package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Benehiko/vee/internal/blockdev"
	"github.com/Benehiko/vee/internal/gpu"
	"github.com/Benehiko/vee/internal/templates"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

// configStep tracks which wizard step is active.
type configStep int

const (
	stepName     configStep = iota // VM name
	stepGPU                        // pick GPU from IOMMU list
	stepNVMe                       // pick NVMe block device
	stepOVMF                       // OVMF_VARS.fd path
	stepVirtiofs                   // virtiofs share dir (optional)
	stepMAC                        // MAC address
	stepConfirm                    // review + submit
)

var configStepCount = int(stepConfirm) + 1

// configModel is the passthrough VM setup wizard.
type configModel struct {
	prov      provider.Provider
	mgr       *vm.Manager
	step      configStep
	autoStart bool

	nameInput   textinput.Model
	gpuDevices  []gpu.PCIDevice
	gpuIdx      int
	gpuErr      string
	nvmeDevices []blockdev.Device
	nvmeIdx     int
	nvmeErr     string
	ovmfInput   textinput.Model
	virtioInput textinput.Model
	macInput    textinput.Model
	submitting  bool
	submitErr   string
}

type configLoadMsg struct {
	gpus    []gpu.PCIDevice
	nvmes   []blockdev.Device
	gpuErr  string
	nvmeErr string
}

type configDoneMsg struct{ err error }

var (
	styleWizTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Padding(0, 1)
	styleWizStep   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleWizLabel  = lipgloss.NewStyle().Width(16).Foreground(lipgloss.Color("7"))
	styleWizVal    = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	styleWizFocus  = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	styleWizHelp   = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Padding(0, 1)
	styleWizErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleWizSelect = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	styleWizDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

func newConfigModel(mgr *vm.Manager, p provider.Provider, autoStart bool, name string) configModel {
	nameIn := textinput.New()
	nameIn.Placeholder = "e.g. arch-gaming"

	ovmfIn := textinput.New()
	ovmfIn.Placeholder = filepath.Join(p.Config().StoragePath, "OVMF_VARS.fd")
	ovmfIn.ShowSuggestions = true

	virtioIn := textinput.New()
	virtioIn.Placeholder = "leave blank to skip"
	virtioIn.ShowSuggestions = true

	macIn := textinput.New()
	macIn.Placeholder = "leave blank for deterministic"

	firstStep := stepName
	if name != "" {
		nameIn.SetValue(name)
		firstStep = stepGPU
	} else {
		nameIn.Focus()
	}

	return configModel{
		prov:        p,
		mgr:         mgr,
		step:        firstStep,
		autoStart:   autoStart,
		nameInput:   nameIn,
		ovmfInput:   ovmfIn,
		virtioInput: virtioIn,
		macInput:    macIn,
	}
}

func (m configModel) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.loadDevices())
}

func (m configModel) loadDevices() tea.Cmd {
	return func() tea.Msg {
		var msg configLoadMsg
		groups, err := gpu.ListIOMMUGroups()
		if err != nil {
			msg.gpuErr = err.Error()
		} else {
			for _, g := range groups {
				for _, d := range g.Devices {
					if d.IsGPU {
						msg.gpus = append(msg.gpus, d)
					}
				}
			}
		}
		nvmes, err := blockdev.ListNVMe()
		if err != nil {
			msg.nvmeErr = err.Error()
		} else {
			msg.nvmes = nvmes
		}
		return msg
	}
}

func (m configModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case configLoadMsg:
		m.gpuDevices = msg.gpus
		m.gpuErr = msg.gpuErr
		m.nvmeDevices = msg.nvmes
		m.nvmeErr = msg.nvmeErr
		return m, nil
	case configDoneMsg:
		if msg.err != nil {
			m.submitErr = msg.err.Error()
			m.submitting = false
			return m, nil
		}
		return m, tea.Quit
	case tea.KeyMsg:
		if m.submitting {
			return m, nil
		}
		return m.handleKey(msg)
	}
	return m.updateInputs(msg)
}

func (m configModel) handleKey(msg tea.KeyMsg) (configModel, tea.Cmd) {
	switch m.step {
	case stepName:
		switch msg.String() {
		case "esc", "ctrl+c":
			return m, tea.Quit
		case "enter":
			if strings.TrimSpace(m.nameInput.Value()) == "" {
				return m, nil
			}
			m.step = stepGPU
			m.nameInput.Blur()
		default:
			var cmd tea.Cmd
			m.nameInput, cmd = m.nameInput.Update(msg)
			return m, cmd
		}
	case stepGPU:
		switch msg.String() {
		case "esc":
			m.step = stepName
			m.nameInput.Focus()
		case "up", "k":
			if m.gpuIdx > 0 {
				m.gpuIdx--
			}
		case "down", "j":
			if m.gpuIdx < len(m.gpuDevices)-1 {
				m.gpuIdx++
			}
		case "enter":
			m.step = stepNVMe
		}
	case stepNVMe:
		switch msg.String() {
		case "esc":
			m.step = stepGPU
		case "up", "k":
			if m.nvmeIdx > 0 {
				m.nvmeIdx--
			}
		case "down", "j":
			if m.nvmeIdx < len(m.nvmeDevices)-1 {
				m.nvmeIdx++
			}
		case "enter":
			m.step = stepOVMF
			m.ovmfInput.Focus()
		}
	case stepOVMF:
		switch msg.String() {
		case "esc":
			m.step = stepNVMe
			m.ovmfInput.Blur()
		case "enter":
			m.step = stepVirtiofs
			m.ovmfInput.Blur()
			m.virtioInput.Focus()
		default:
			var cmd tea.Cmd
			m.ovmfInput, cmd = m.ovmfInput.Update(msg)
			m.ovmfInput.SetSuggestions(pathSuggestions(m.ovmfInput.Value()))
			return m, cmd
		}
	case stepVirtiofs:
		switch msg.String() {
		case "esc":
			m.step = stepOVMF
			m.virtioInput.Blur()
			m.ovmfInput.Focus()
		case "enter":
			m.step = stepMAC
			m.virtioInput.Blur()
			m.macInput.Focus()
		default:
			var cmd tea.Cmd
			m.virtioInput, cmd = m.virtioInput.Update(msg)
			m.virtioInput.SetSuggestions(pathSuggestions(m.virtioInput.Value()))
			return m, cmd
		}
	case stepMAC:
		switch msg.String() {
		case "esc":
			m.step = stepVirtiofs
			m.macInput.Blur()
			m.virtioInput.Focus()
		case "enter":
			m.step = stepConfirm
			m.macInput.Blur()
		default:
			var cmd tea.Cmd
			m.macInput, cmd = m.macInput.Update(msg)
			return m, cmd
		}
	case stepConfirm:
		switch msg.String() {
		case "esc":
			m.step = stepMAC
			m.macInput.Focus()
		case "enter", "y":
			m.submitting = true
			return m, m.doSubmit()
		case "n", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m configModel) updateInputs(msg tea.Msg) (configModel, tea.Cmd) {
	var cmd tea.Cmd
	switch m.step {
	case stepName:
		m.nameInput, cmd = m.nameInput.Update(msg)
	case stepOVMF:
		m.ovmfInput, cmd = m.ovmfInput.Update(msg)
		m.ovmfInput.SetSuggestions(pathSuggestions(m.ovmfInput.Value()))
	case stepVirtiofs:
		m.virtioInput, cmd = m.virtioInput.Update(msg)
		m.virtioInput.SetSuggestions(pathSuggestions(m.virtioInput.Value()))
	case stepMAC:
		m.macInput, cmd = m.macInput.Update(msg)
	}
	return m, cmd
}

func (m configModel) selectedGPUAddr() string {
	if len(m.gpuDevices) == 0 {
		return ""
	}
	return m.gpuDevices[m.gpuIdx].Address
}

func (m configModel) selectedNVMePath() string {
	if len(m.nvmeDevices) == 0 {
		return ""
	}
	return m.nvmeDevices[m.nvmeIdx].ByIDPath
}

func (m configModel) View() string {
	var sb strings.Builder
	stepLabel := fmt.Sprintf("Step %d/%d", int(m.step)+1, configStepCount)
	sb.WriteString(styleWizTitle.Render("  Passthrough VM Setup  "))
	sb.WriteString(styleWizStep.Render("  " + stepLabel))
	sb.WriteString("\n\n")

	switch m.step {
	case stepName:
		sb.WriteString(styleWizLabel.Render("VM name") + "\n")
		sb.WriteString("  " + m.nameInput.View() + "\n")
	case stepGPU:
		sb.WriteString(styleWizLabel.Render("GPU (VFIO)") + "\n\n")
		switch {
		case m.gpuErr != "":
			sb.WriteString(styleWizErr.Render("  "+m.gpuErr) + "\n")
			sb.WriteString(styleWizDim.Render("  (no GPU selected — edit pci_addr in vm.yaml later)") + "\n")
		case len(m.gpuDevices) == 0:
			sb.WriteString(styleWizDim.Render("  No GPUs found in IOMMU groups") + "\n")
		default:
			for i, d := range m.gpuDevices {
				label := gpu.GPULabel(d)
				driver := d.Driver
				if driver == "" {
					driver = "none"
				}
				line := fmt.Sprintf("%s  driver: %s", label, driver)
				if i == m.gpuIdx {
					sb.WriteString(styleWizSelect.Render("▶ " + line))
				} else {
					sb.WriteString(styleWizDim.Render("  " + line))
				}
				sb.WriteString("\n")
			}
		}
	case stepNVMe:
		sb.WriteString(styleWizLabel.Render("NVMe device") + "\n\n")
		switch {
		case m.nvmeErr != "":
			sb.WriteString(styleWizErr.Render("  "+m.nvmeErr) + "\n")
		case len(m.nvmeDevices) == 0:
			sb.WriteString(styleWizDim.Render("  No NVMe devices found") + "\n")
		default:
			for i, d := range m.nvmeDevices {
				label := d.Label()
				if i == m.nvmeIdx {
					sb.WriteString(styleWizSelect.Render("▶ " + label))
				} else {
					sb.WriteString(styleWizDim.Render("  " + label))
				}
				sb.WriteString("\n")
			}
		}
	case stepOVMF:
		sb.WriteString(styleWizLabel.Render("OVMF_VARS.fd") + "\n")
		sb.WriteString(styleWizDim.Render("  Existing UEFI vars with boot entries (copied to VM dir)") + "\n\n")
		sb.WriteString("  " + m.ovmfInput.View() + "\n")
	case stepVirtiofs:
		sb.WriteString(styleWizLabel.Render("Virtiofs dir") + "\n")
		sb.WriteString(styleWizDim.Render("  Host directory shared as 'Games' tag (leave blank to skip)") + "\n\n")
		sb.WriteString("  " + m.virtioInput.View() + "\n")
	case stepMAC:
		sb.WriteString(styleWizLabel.Render("MAC address") + "\n")
		sb.WriteString(styleWizDim.Render("  Bridge NIC MAC (leave blank for deterministic from VM name)") + "\n\n")
		sb.WriteString("  " + m.macInput.View() + "\n")
	case stepConfirm:
		sb.WriteString(m.renderSummary())
		sb.WriteString("\n")
		if m.submitErr != "" {
			sb.WriteString(styleWizErr.Render("  Error: "+m.submitErr) + "\n\n")
		}
		if m.submitting {
			sb.WriteString(styleWizFocus.Render("  Creating…") + "\n")
		} else {
			sb.WriteString(styleWizHelp.Render("  enter/y to create  n to cancel  esc to go back"))
		}
		return sb.String()
	}

	sb.WriteString("\n")
	sb.WriteString(styleWizHelp.Render("enter: next  esc: back  ↑↓/jk: move  tab: complete"))
	return sb.String()
}

func (m configModel) renderSummary() string {
	var sb strings.Builder
	row := func(label, val string) {
		sb.WriteString("  " + styleWizLabel.Render(label) + styleWizVal.Render(val) + "\n")
	}
	row("Name", m.nameInput.Value())
	if addr := m.selectedGPUAddr(); addr != "" && len(m.gpuDevices) > 0 {
		row("GPU", gpu.GPULabel(m.gpuDevices[m.gpuIdx]))
	} else {
		row("GPU", "(none)")
	}
	row("NVMe", m.selectedNVMePath())
	ovmf := m.ovmfInput.Value()
	if ovmf == "" {
		ovmf = m.ovmfInput.Placeholder
	}
	row("OVMF vars", ovmf)
	vdir := m.virtioInput.Value()
	if vdir == "" {
		vdir = "(skipped)"
	}
	row("Virtiofs dir", vdir)
	mac := m.macInput.Value()
	if mac == "" {
		mac = "(deterministic)"
	}
	row("MAC", mac)
	autoS := "no"
	if m.autoStart {
		autoS = "yes"
	}
	row("Auto-start", autoS)
	return sb.String()
}

func (m configModel) doSubmit() tea.Cmd {
	name := strings.TrimSpace(m.nameInput.Value())
	nvmeDev := m.selectedNVMePath()
	ovmf := strings.TrimSpace(m.ovmfInput.Value())
	if ovmf == "" {
		ovmf = m.ovmfInput.Placeholder
	}
	pciAddr := m.selectedGPUAddr()
	virtioDir := strings.TrimSpace(m.virtioInput.Value())
	mac := strings.TrimSpace(m.macInput.Value())
	autoStart := m.autoStart
	prov := m.prov
	mgr := m.mgr

	return func() tea.Msg {
		cfg := templates.NewPassthroughConfig(prov, name, nvmeDev, ovmf, pciAddr, virtioDir, mac)
		if err := mgr.Create(context.Background(), cfg); err != nil {
			return configDoneMsg{err: err}
		}
		if autoStart {
			if err := mgr.Start(context.Background(), name, false); err != nil {
				return configDoneMsg{err: fmt.Errorf("created OK, start failed: %w", err)}
			}
		}
		return configDoneMsg{}
	}
}

// RunConfigWizard launches the passthrough creation wizard as a standalone program.
// If name is non-empty the wizard skips the name step and pre-fills it.
func RunConfigWizard(ctx context.Context, p provider.Provider, autoStart bool, name string) error {
	mgr := vm.NewManager(p)
	m := newConfigModel(mgr, p, autoStart, name)
	prog := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := prog.Run()
	return err
}
