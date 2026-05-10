package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Benehiko/vee/internal/blockdev"
	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/templates"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
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
	"docker",
	"torrent",
	"gaming",
	"passthrough",
	"windows",
}

// distroAwareTemplates are templates that support --distro selection.
var distroAwareTemplates = map[string]bool{
	"devbox": true,
	"server": true,
}

// createField identifies which form field is focused.
type createField int

const (
	fieldName createField = iota
	fieldTemplate
	fieldDistro
	fieldDistroVersion
	fieldMemory
	fieldCPUs
	fieldDiskMode
	fieldDiskSize
	fieldDiskDev
	fieldCount
)

type diskMode int

const (
	diskModeNew         diskMode = iota // create a new qcow2 image
	diskModePassthrough                 // raw block device passthrough
	diskModeNone                        // no extra disk (template default only)
)

var diskModeNames = []string{"new", "passthrough", "none"}

type createModel struct {
	mgr          *vm.Manager
	prov         provider.Provider
	field        createField
	name         string
	tmplIdx      int
	distroIdx    int
	distroVerIdx int
	memory       string
	cpus         string
	dMode        diskMode
	diskSize     string
	blockDevs    []blockdev.Device
	blockDevIdx  int
	err          string
	submitting   bool
}

type createDoneMsg struct{ err error }

func newCreateModel(mgr *vm.Manager, p provider.Provider) createModel {
	devs, _ := blockdev.ListUnmounted()
	return createModel{
		mgr:       mgr,
		prov:      p,
		memory:    "2G",
		cpus:      "2",
		dMode:     diskModeNone,
		diskSize:  "20G",
		blockDevs: devs,
	}
}

func (m createModel) selectedDistro() string {
	distros := images.SupportedDistros()
	if m.distroIdx >= len(distros) {
		return distros[0]
	}
	return distros[m.distroIdx]
}

func (m createModel) selectedDistroVersion() string {
	versions := images.DistroVersions(m.selectedDistro())
	if len(versions) == 0 {
		return "latest"
	}
	if m.distroVerIdx >= len(versions) {
		return versions[0]
	}
	return versions[m.distroVerIdx]
}

func (m createModel) isDistroAware() bool {
	if m.tmplIdx >= len(templateNames) {
		return false
	}
	return distroAwareTemplates[templateNames[m.tmplIdx]]
}

func (m createModel) Init() tea.Cmd { return nil }

func (m createModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.KeyMsg:
		if m.submitting {
			return m, nil
		}

		// Skip distro/version fields for non-distro-aware templates,
		// and skip disk size/dev fields based on selected disk mode.
		nextField := func(cur createField, delta int) createField {
			n := int(cur) + delta
			for {
				f := createField((n + int(fieldCount)) % int(fieldCount))
				if (f == fieldDistro || f == fieldDistroVersion) && !m.isDistroAware() {
					n += delta
					continue
				}
				if f == fieldDiskSize && m.dMode != diskModeNew {
					n += delta
					continue
				}
				if f == fieldDiskDev && m.dMode != diskModePassthrough {
					n += delta
					continue
				}
				return f
			}
		}

		switch msg.String() {
		case "esc":
			return m, gotoList()
		case "tab", "down":
			m.field = nextField(m.field, 1)
		case "shift+tab", "up":
			m.field = nextField(m.field, -1)
		case "enter":
			isLast := m.isLastField()
			if isLast {
				m.submitting = true
				return m, m.doSubmit()
			}
			m.field = nextField(m.field, 1)
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
			case fieldDiskSize:
				if len(m.diskSize) > 0 {
					m.diskSize = m.diskSize[:len(m.diskSize)-1]
				}
			}
		case "left":
			switch m.field {
			case fieldTemplate:
				if m.tmplIdx > 0 {
					m.tmplIdx--
					m.distroIdx = 0
					m.distroVerIdx = 0
				}
			case fieldDistro:
				if m.distroIdx > 0 {
					m.distroIdx--
					m.distroVerIdx = 0
				}
			case fieldDistroVersion:
				if m.distroVerIdx > 0 {
					m.distroVerIdx--
				}
			case fieldDiskMode:
				if m.dMode > diskModeNew {
					m.dMode--
				}
			case fieldDiskDev:
				if m.blockDevIdx > 0 {
					m.blockDevIdx--
				}
			}
		case "right":
			switch m.field {
			case fieldTemplate:
				if m.tmplIdx < len(templateNames)-1 {
					m.tmplIdx++
					m.distroIdx = 0
					m.distroVerIdx = 0
				}
			case fieldDistro:
				distros := images.SupportedDistros()
				if m.distroIdx < len(distros)-1 {
					m.distroIdx++
					m.distroVerIdx = 0
				}
			case fieldDistroVersion:
				versions := images.DistroVersions(m.selectedDistro())
				if m.distroVerIdx < len(versions)-1 {
					m.distroVerIdx++
				}
			case fieldDiskMode:
				if int(m.dMode) < len(diskModeNames)-1 {
					m.dMode++
				}
			case fieldDiskDev:
				if m.blockDevIdx < len(m.blockDevs)-1 {
					m.blockDevIdx++
				}
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
				case fieldDiskSize:
					m.diskSize += ch
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

	type fieldDef struct {
		label string
		value string
		f     createField
		skip  bool
	}

	fields := []fieldDef{
		{"Name", m.name + cursor(m.field == fieldName), fieldName, false},
		{"Template", templateSelector(m.tmplIdx, m.field == fieldTemplate), fieldTemplate, false},
		{"Distro", distroSelector(m.distroIdx, m.field == fieldDistro), fieldDistro, !m.isDistroAware()},
		{"Version", versionSelector(m.selectedDistro(), m.distroVerIdx, m.field == fieldDistroVersion), fieldDistroVersion, !m.isDistroAware()},
		{"Memory", m.memory + cursor(m.field == fieldMemory), fieldMemory, false},
		{"CPUs", m.cpus + cursor(m.field == fieldCPUs), fieldCPUs, false},
		{"Disk", diskModeSelector(m.dMode, m.field == fieldDiskMode), fieldDiskMode, false},
		{"Disk Size", m.diskSize + cursor(m.field == fieldDiskSize), fieldDiskSize, m.dMode != diskModeNew},
		{"Disk Device", diskDevSelector(m.blockDevs, m.blockDevIdx, m.field == fieldDiskDev), fieldDiskDev, m.dMode != diskModePassthrough},
	}

	for _, f := range fields {
		if f.skip {
			continue
		}
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

	sb.WriteString(styleFormHelp.Render("tab/↑↓ navigate  ←/→ choose option  enter submit  esc cancel"))
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

func distroSelector(idx int, focused bool) string {
	distros := images.SupportedDistros()
	var parts []string
	for i, d := range distros {
		if i == idx {
			if focused {
				parts = append(parts, styleFieldFocus.Render("[ "+d+" ]"))
			} else {
				parts = append(parts, styleFieldValue.Render("[ "+d+" ]"))
			}
		} else {
			parts = append(parts, styleFaint.Render(d))
		}
	}
	return strings.Join(parts, " ")
}

func versionSelector(distro string, idx int, focused bool) string {
	versions := images.DistroVersions(distro)
	if len(versions) == 0 {
		return "latest"
	}
	var parts []string
	for i, v := range versions {
		if i == idx {
			if focused {
				parts = append(parts, styleFieldFocus.Render("[ "+v+" ]"))
			} else {
				parts = append(parts, styleFieldValue.Render("[ "+v+" ]"))
			}
		} else {
			parts = append(parts, styleFaint.Render(v))
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

// isLastField reports whether the current field is the final one given the
// selected disk mode, so Enter knows to submit rather than advance.
func (m createModel) isLastField() bool {
	switch m.dMode {
	case diskModeNew:
		return m.field == fieldDiskSize
	case diskModePassthrough:
		return m.field == fieldDiskDev
	default:
		return m.field == fieldDiskMode
	}
}

func diskModeSelector(mode diskMode, focused bool) string {
	var parts []string
	for i, n := range diskModeNames {
		if diskMode(i) == mode {
			if focused {
				parts = append(parts, styleFieldFocus.Render("[ "+n+" ]"))
			} else {
				parts = append(parts, styleFieldValue.Render("[ "+n+" ]"))
			}
		} else {
			parts = append(parts, styleFaint.Render(n))
		}
	}
	return strings.Join(parts, " ")
}

func diskDevSelector(devs []blockdev.Device, idx int, focused bool) string {
	if len(devs) == 0 {
		return styleFaint.Render("(no unmounted devices)")
	}
	var parts []string
	for i, d := range devs {
		label := d.Label()
		if i == idx {
			if focused {
				parts = append(parts, styleFieldFocus.Render("[ "+label+" ]"))
			} else {
				parts = append(parts, styleFieldValue.Render("[ "+label+" ]"))
			}
		} else {
			parts = append(parts, styleFaint.Render(d.ByIDPath))
		}
	}
	return strings.Join(parts, " ")
}

func (m createModel) doSubmit() tea.Cmd {
	name := m.name
	tmpl := templateNames[m.tmplIdx]
	memory := m.memory
	cpus := m.cpus
	distro := m.selectedDistro()
	distroVer := m.selectedDistroVersion()
	dMode := m.dMode
	diskSize := m.diskSize
	var blockDevPath string
	if m.dMode == diskModePassthrough && len(m.blockDevs) > 0 {
		blockDevPath = m.blockDevs[m.blockDevIdx].ByIDPath
	}

	mgr := m.mgr
	prov := m.prov

	return func() tea.Msg {
		cfg, err := buildConfig(context.Background(), prov, mgr, name, tmpl, memory, cpus, distro, distroVer, dMode, diskSize, blockDevPath)
		if err != nil {
			return createDoneMsg{err: err}
		}
		if err := mgr.Create(context.Background(), cfg); err != nil {
			return createDoneMsg{err: err}
		}
		return createDoneMsg{}
	}
}

func buildConfig(ctx context.Context, p provider.Provider, mgr *vm.Manager, name, tmpl, memory, cpusStr, distro, distroVer string, dMode diskMode, diskSize, blockDevPath string) (*vm.VMConfig, error) {
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}

	var cpus int
	if _, err := fmt.Sscan(cpusStr, &cpus); err != nil || cpus < 1 {
		cpus = 2
	}

	var cfg *vm.VMConfig
	switch tmpl {
	case "gaming", "gaming-arch":
		var err error
		cfg, err = templates.NewGamingArchConfig(ctx, p, name, nil, templates.GamingOptions{})
		if err != nil {
			return nil, err
		}
	case "gaming-bazzite":
		var err error
		cfg, err = templates.NewGamingBazziteConfig(ctx, p, name, templates.GamingOptions{})
		if err != nil {
			return nil, err
		}
	case "torrent":
		var err error
		cfg, err = templates.NewTorrentConfig(ctx, p, name, nil, nil, nil, nil, "", 0)
		if err != nil {
			return nil, err
		}
	case "devbox":
		var err error
		cfg, err = templates.NewDevboxConfig(ctx, p, name, nil, distro, distroVer)
		if err != nil {
			return nil, err
		}
	case "server":
		var err error
		cfg, err = templates.NewServerConfig(ctx, p, name, nil, distro, distroVer)
		if err != nil {
			return nil, err
		}
	case "docker":
		var err error
		cfg, err = templates.NewDockerConfig(ctx, p, name, nil, distroVer)
		if err != nil {
			return nil, err
		}
	case "windows":
		var err error
		cfg, err = templates.NewWindowsConfig(ctx, p, images.Windows11, name)
		if err != nil {
			return nil, err
		}
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

	switch dMode {
	case diskModeNew:
		if diskSize != "" {
			cfg.Disks = append([]vm.DiskConfig{{
				Size:      diskSize,
				Format:    "qcow2",
				Interface: "virtio",
				Media:     "disk",
			}}, cfg.Disks...)
		}
	case diskModePassthrough:
		if blockDevPath != "" {
			cfg.Disks = append(cfg.Disks, vm.DiskConfig{
				Path:        blockDevPath,
				Passthrough: true,
			})
		}
	}

	_ = mgr
	return cfg, nil
}
