package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Benehiko/vee/internal/blockdev"
	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/internal/vm/build"
	"github.com/Benehiko/vee/provider"
)

var (
	styleFormTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Padding(0, 1)
	styleFieldLabel = lipgloss.NewStyle().Width(16).Foreground(lipgloss.Color("7"))
	styleFieldValue = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	styleFieldFocus = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	styleFormHelp   = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Padding(0, 1)
	styleFormErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleSection    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13")).Padding(0, 1)
)

var templateNames = []string{
	"ubuntu-server",
	"devbox",
	"server",
	"docker",
	"torrent",
	"gaming-arch",
	"gaming-bazzite",
	"gaming",
	"passthrough",
	"truenas",
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
	// Basics — always visible.
	fieldName createField = iota
	fieldTemplate
	fieldDistro
	fieldDistroVersion
	fieldMemory
	fieldCPUs
	fieldDiskMode
	fieldDiskSize
	fieldDiskDev

	// Advanced toggle row — focusable; pressing space/enter expands.
	fieldAdvancedToggle

	// Advanced — visible only when advancedOpen.
	fieldNICMode
	fieldNICBridge
	fieldNICMAC
	fieldHostname
	fieldHeadless
	fieldUEFI
	fieldGPUMode
	fieldGPUPCI
	fieldGPUVendor
	fieldAntiDetect
	fieldVirtiofsDir
	fieldVirtiofsTag
	fieldSPICEPort
	fieldSSHPort
	fieldSSHShare
	fieldSSHKeyFile

	fieldCount
)

type diskMode int

const (
	diskModeNew         diskMode = iota // create a new qcow2 image
	diskModePassthrough                 // raw block device passthrough
	diskModeNone                        // no extra disk (template default only)
)

var diskModeNames = []string{"new", "passthrough", "none"}

var (
	nicModeNames   = []string{"user", "bridge"}
	gpuModeNames   = []string{"none", "virtio", "passthrough"}
	gpuVendorNames = []string{"amd", "nvidia", "virtio"}
)

// createModel is the form for `vee create` in the TUI. Every flag exposed by
// the cobra `vee create` command has a corresponding field here. Less-common
// fields are hidden behind an "Advanced" toggle to keep the default form
// compact.
type createModel struct {
	mgr  *vm.Manager
	prov provider.Provider

	field        createField
	advancedOpen bool

	// Basics.
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

	// Advanced.
	nicModeIdx   int
	nicBridge    string
	nicMAC       string
	hostname     string
	headless     bool
	uefi         bool
	gpuModeIdx   int
	gpuPCI       string
	gpuVendorIdx int
	antiDetect   bool
	virtiofsDir  string
	virtiofsTag  string
	spicePort    string
	sshPort      string
	sshShare     bool
	sshKeyFile   string

	err        string
	submitting bool
}

type createDoneMsg struct{ err error }

func newCreateModel(mgr *vm.Manager, p provider.Provider) createModel {
	devs, _ := blockdev.ListUnmounted()
	return createModel{
		mgr:         mgr,
		prov:        p,
		memory:      "2G",
		cpus:        "2",
		dMode:       diskModeNone,
		diskSize:    "20G",
		blockDevs:   devs,
		virtiofsTag: "share",
	}
}

// applyPrefill populates the form from a build.Opts so flags supplied on the
// command line show up pre-filled when the TUI is opened from the
// `vee create <name> --foo bar` (without --template) entry point.
func (m *createModel) applyPrefill(o build.Opts) {
	if o.Name != "" {
		m.name = o.Name
	}
	if o.Template != "" {
		for i, t := range templateNames {
			if t == o.Template {
				m.tmplIdx = i
				break
			}
		}
	}
	if o.Memory != "" {
		m.memory = o.Memory
	}
	if o.CPUs > 0 {
		m.cpus = fmt.Sprintf("%d", o.CPUs)
	}
	if o.Distro != "" {
		distros := images.SupportedDistros()
		for i, d := range distros {
			if d == o.Distro {
				m.distroIdx = i
				break
			}
		}
	}
	if o.DistroVersion != "" {
		versions := images.DistroVersions(m.selectedDistro())
		for i, v := range versions {
			if v == o.DistroVersion {
				m.distroVerIdx = i
				break
			}
		}
	}
	if o.Disk != "" {
		m.diskSize = o.Disk
		m.dMode = diskModeNew
	}
	if o.NICMode != "" {
		for i, n := range nicModeNames {
			if n == o.NICMode {
				m.nicModeIdx = i
				m.advancedOpen = true
				break
			}
		}
	}
	if o.NICBridge != "" {
		m.nicBridge = o.NICBridge
		m.advancedOpen = true
	}
	if o.NICMAC != "" {
		m.nicMAC = o.NICMAC
		m.advancedOpen = true
	}
	if o.Hostname != "" {
		m.hostname = o.Hostname
		m.advancedOpen = true
	}
	if o.Headless != nil {
		m.headless = *o.Headless
		m.advancedOpen = true
	}
	if o.UEFI != nil {
		m.uefi = *o.UEFI
		m.advancedOpen = true
	}
	if o.GPUMode != "" {
		for i, n := range gpuModeNames {
			if n == o.GPUMode {
				m.gpuModeIdx = i
				m.advancedOpen = true
				break
			}
		}
	}
	if o.GPUPCI != "" {
		m.gpuPCI = o.GPUPCI
		m.advancedOpen = true
	}
	if o.GPUVendor != "" {
		for i, n := range gpuVendorNames {
			if n == o.GPUVendor {
				m.gpuVendorIdx = i
				m.advancedOpen = true
				break
			}
		}
	}
	if o.AntiDetect != nil {
		m.antiDetect = *o.AntiDetect
		m.advancedOpen = true
	}
	if o.VirtiofsDir != "" {
		m.virtiofsDir = o.VirtiofsDir
		m.advancedOpen = true
	}
	if o.VirtiofsTag != "" {
		m.virtiofsTag = o.VirtiofsTag
	}
	if o.SPICEPort != nil && *o.SPICEPort > 0 {
		m.spicePort = fmt.Sprintf("%d", *o.SPICEPort)
		m.advancedOpen = true
	}
	if o.SSHPort > 0 {
		m.sshPort = fmt.Sprintf("%d", o.SSHPort)
		m.advancedOpen = true
	}
	if o.SSHShare != nil {
		m.sshShare = *o.SSHShare
		m.advancedOpen = true
	}
	if o.SSHKeyFile != "" {
		m.sshKeyFile = o.SSHKeyFile
		m.advancedOpen = true
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

// fieldVisible reports whether a field is currently displayed and therefore
// reachable by Tab/Shift-Tab navigation.
func (m createModel) fieldVisible(f createField) bool {
	switch f {
	case fieldDistro, fieldDistroVersion:
		return m.isDistroAware()
	case fieldDiskSize:
		return m.dMode == diskModeNew
	case fieldDiskDev:
		return m.dMode == diskModePassthrough
	case fieldAdvancedToggle:
		return true
	}
	if f >= fieldNICMode {
		return m.advancedOpen
	}
	return true
}

func (m createModel) Init() tea.Cmd { return nil }

func (m createModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.KeyMsg:
		if m.submitting {
			return m, nil
		}

		nextField := func(cur createField, delta int) createField {
			n := int(cur) + delta
			for i := 0; i < int(fieldCount)*2; i++ {
				f := createField((n + int(fieldCount)) % int(fieldCount))
				if m.fieldVisible(f) {
					return f
				}
				n += delta
			}
			return cur
		}

		switch msg.String() {
		case "esc":
			return m, gotoList()
		case "tab", "down":
			m.field = nextField(m.field, 1)
		case "shift+tab", "up":
			m.field = nextField(m.field, -1)
		case "enter":
			if m.field == fieldAdvancedToggle {
				m.advancedOpen = !m.advancedOpen
				return m, nil
			}
			// Submit when on the final visible field, otherwise advance.
			if m.field == m.lastVisibleField() {
				m.submitting = true
				return m, m.doSubmit()
			}
			m.field = nextField(m.field, 1)
		case " ":
			// Space toggles boolean-style fields and the advanced section.
			switch m.field {
			case fieldAdvancedToggle:
				m.advancedOpen = !m.advancedOpen
			case fieldHeadless:
				m.headless = !m.headless
			case fieldUEFI:
				m.uefi = !m.uefi
			case fieldAntiDetect:
				m.antiDetect = !m.antiDetect
			case fieldSSHShare:
				m.sshShare = !m.sshShare
			default:
				// Allow space inside text inputs.
				m.appendChar(" ")
			}
		case "backspace":
			m.removeChar()
		case "left":
			m.adjustSelector(-1)
		case "right":
			m.adjustSelector(+1)
		default:
			ch := msg.String()
			if len(ch) == 1 {
				m.appendChar(ch)
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

// appendChar adds a literal character to whichever text field is focused.
func (m *createModel) appendChar(ch string) {
	switch m.field {
	case fieldName:
		m.name += ch
	case fieldMemory:
		m.memory += ch
	case fieldCPUs:
		m.cpus += ch
	case fieldDiskSize:
		m.diskSize += ch
	case fieldNICBridge:
		m.nicBridge += ch
	case fieldNICMAC:
		m.nicMAC += ch
	case fieldHostname:
		m.hostname += ch
	case fieldGPUPCI:
		m.gpuPCI += ch
	case fieldVirtiofsDir:
		m.virtiofsDir += ch
	case fieldVirtiofsTag:
		m.virtiofsTag += ch
	case fieldSPICEPort:
		m.spicePort += ch
	case fieldSSHPort:
		m.sshPort += ch
	case fieldSSHKeyFile:
		m.sshKeyFile += ch
	}
}

func (m *createModel) removeChar() {
	trim := func(s string) string {
		if len(s) == 0 {
			return s
		}
		return s[:len(s)-1]
	}
	switch m.field {
	case fieldName:
		m.name = trim(m.name)
	case fieldMemory:
		m.memory = trim(m.memory)
	case fieldCPUs:
		m.cpus = trim(m.cpus)
	case fieldDiskSize:
		m.diskSize = trim(m.diskSize)
	case fieldNICBridge:
		m.nicBridge = trim(m.nicBridge)
	case fieldNICMAC:
		m.nicMAC = trim(m.nicMAC)
	case fieldHostname:
		m.hostname = trim(m.hostname)
	case fieldGPUPCI:
		m.gpuPCI = trim(m.gpuPCI)
	case fieldVirtiofsDir:
		m.virtiofsDir = trim(m.virtiofsDir)
	case fieldVirtiofsTag:
		m.virtiofsTag = trim(m.virtiofsTag)
	case fieldSPICEPort:
		m.spicePort = trim(m.spicePort)
	case fieldSSHPort:
		m.sshPort = trim(m.sshPort)
	case fieldSSHKeyFile:
		m.sshKeyFile = trim(m.sshKeyFile)
	}
}

func (m *createModel) adjustSelector(delta int) {
	switch m.field {
	case fieldTemplate:
		m.tmplIdx = clampIdx(m.tmplIdx+delta, len(templateNames))
		m.distroIdx, m.distroVerIdx = 0, 0
	case fieldDistro:
		m.distroIdx = clampIdx(m.distroIdx+delta, len(images.SupportedDistros()))
		m.distroVerIdx = 0
	case fieldDistroVersion:
		m.distroVerIdx = clampIdx(m.distroVerIdx+delta, len(images.DistroVersions(m.selectedDistro())))
	case fieldDiskMode:
		m.dMode = diskMode(clampIdx(int(m.dMode)+delta, len(diskModeNames)))
	case fieldDiskDev:
		m.blockDevIdx = clampIdx(m.blockDevIdx+delta, len(m.blockDevs))
	case fieldNICMode:
		m.nicModeIdx = clampIdx(m.nicModeIdx+delta, len(nicModeNames))
	case fieldGPUMode:
		m.gpuModeIdx = clampIdx(m.gpuModeIdx+delta, len(gpuModeNames))
	case fieldGPUVendor:
		m.gpuVendorIdx = clampIdx(m.gpuVendorIdx+delta, len(gpuVendorNames))
	}
}

func clampIdx(i, max int) int {
	if max == 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i >= max {
		return max - 1
	}
	return i
}

// lastVisibleField walks fields backwards from fieldCount-1 and returns the
// first one that's currently visible — that's where Enter triggers submit.
func (m createModel) lastVisibleField() createField {
	for f := fieldCount - 1; f >= 0; f-- {
		if m.fieldVisible(f) {
			return f
		}
	}
	return fieldName
}

type fieldDef struct {
	label string
	value string
	f     createField
	skip  bool
}

func (m createModel) View() string {
	var sb strings.Builder

	sb.WriteString(styleFormTitle.Render("  Create VM  "))
	sb.WriteString("\n\n")

	basics := []fieldDef{
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
	m.renderFields(&sb, basics)

	// Advanced toggle.
	chevron := "[+]"
	if m.advancedOpen {
		chevron = "[−]"
	}
	advLabel := chevron + " Advanced"
	if m.field == fieldAdvancedToggle {
		sb.WriteString("  " + styleFieldFocus.Render(advLabel) + "\n")
	} else {
		sb.WriteString("  " + styleSection.Render(advLabel) + "\n")
	}

	if m.advancedOpen {
		advanced := []fieldDef{
			{"NIC Mode", selectorNamed(nicModeNames, m.nicModeIdx, m.field == fieldNICMode), fieldNICMode, false},
			{"NIC Bridge", m.nicBridge + cursor(m.field == fieldNICBridge), fieldNICBridge, false},
			{"NIC MAC", m.nicMAC + cursor(m.field == fieldNICMAC), fieldNICMAC, false},
			{"Hostname", m.hostname + cursor(m.field == fieldHostname), fieldHostname, false},
			{"Headless", boolValue(m.headless, m.field == fieldHeadless), fieldHeadless, false},
			{"UEFI", boolValue(m.uefi, m.field == fieldUEFI), fieldUEFI, false},
			{"GPU Mode", selectorNamed(gpuModeNames, m.gpuModeIdx, m.field == fieldGPUMode), fieldGPUMode, false},
			{"GPU PCI", m.gpuPCI + cursor(m.field == fieldGPUPCI), fieldGPUPCI, false},
			{"GPU Vendor", selectorNamed(gpuVendorNames, m.gpuVendorIdx, m.field == fieldGPUVendor), fieldGPUVendor, false},
			{"Anti-detect", boolValue(m.antiDetect, m.field == fieldAntiDetect), fieldAntiDetect, false},
			{"Virtiofs Dir", m.virtiofsDir + cursor(m.field == fieldVirtiofsDir), fieldVirtiofsDir, false},
			{"Virtiofs Tag", m.virtiofsTag + cursor(m.field == fieldVirtiofsTag), fieldVirtiofsTag, false},
			{"SPICE Port", m.spicePort + cursor(m.field == fieldSPICEPort), fieldSPICEPort, false},
			{"SSH Port", m.sshPort + cursor(m.field == fieldSSHPort), fieldSSHPort, false},
			{"SSH Share", boolValue(m.sshShare, m.field == fieldSSHShare), fieldSSHShare, false},
			{"SSH Keys", m.sshKeyFile + cursor(m.field == fieldSSHKeyFile), fieldSSHKeyFile, false},
		}
		m.renderFields(&sb, advanced)
	}

	sb.WriteString("\n")
	if m.err != "" {
		sb.WriteString(styleFormErr.Render("  Error: "+m.err) + "\n\n")
	}
	if m.submitting {
		sb.WriteString(styleFieldFocus.Render("  Creating…") + "\n")
	}

	sb.WriteString(styleFormHelp.Render("tab/↑↓ navigate  ←/→ choose option  space toggle  enter submit  esc cancel"))
	return sb.String()
}

func (m createModel) renderFields(sb *strings.Builder, fields []fieldDef) {
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
}

func selectorNamed(names []string, idx int, focused bool) string {
	var parts []string
	for i, n := range names {
		if i == idx {
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

func boolValue(v, focused bool) string {
	s := "off"
	if v {
		s = "on"
	}
	if focused {
		return styleFieldFocus.Render("[ " + s + " ]")
	}
	return styleFieldValue.Render("[ " + s + " ]")
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

// doSubmit assembles a build.Opts from the form state, calls build.Build, and
// persists via vm.Manager.Create. Mirrors what cmd/create.go does in flag mode.
func (m createModel) doSubmit() tea.Cmd {
	opts := m.toBuildOpts()
	mgr := m.mgr
	prov := m.prov

	return func() tea.Msg {
		ctx := context.Background()
		if opts.Template == "torrent" {
			return createDoneMsg{err: fmt.Errorf("torrent template requires interactive prompts; use the CLI: vee create %s --template torrent", opts.Name)}
		}
		cfg, err := build.Build(ctx, prov, opts)
		if err != nil {
			return createDoneMsg{err: err}
		}
		// Disk passthrough: the TUI exposes a single block device. Append it
		// here rather than going through --disk-style overrides.
		if m.dMode == diskModePassthrough && len(m.blockDevs) > 0 {
			cfg.Disks = append(cfg.Disks, vm.DiskConfig{
				Path:        m.blockDevs[m.blockDevIdx].ByIDPath,
				Passthrough: true,
			})
		}
		if err := mgr.Create(ctx, cfg); err != nil {
			return createDoneMsg{err: err}
		}
		return createDoneMsg{}
	}
}

func (m createModel) toBuildOpts() build.Opts {
	opts := build.Opts{
		Name:     m.name,
		Template: templateNames[m.tmplIdx],
	}
	if m.memory != "" {
		opts.Memory = m.memory
	}
	if cpus := parseInt(m.cpus); cpus > 0 {
		opts.CPUs = cpus
	}
	if m.isDistroAware() {
		opts.Distro = m.selectedDistro()
		opts.DistroVersion = m.selectedDistroVersion()
	} else {
		// For non-distro-aware templates, the distro version field is reused
		// as the image version pin (e.g. ubuntu-server, windows, docker).
		opts.DistroVersion = m.selectedDistroVersion()
	}
	if m.dMode == diskModeNew && m.diskSize != "" {
		opts.Disk = m.diskSize
	}
	// Advanced.
	if m.advancedOpen {
		if mode := nicModeNames[m.nicModeIdx]; mode != "" {
			opts.NICMode = mode
		}
		if m.nicBridge != "" {
			opts.NICBridge = m.nicBridge
		}
		if m.nicMAC != "" {
			opts.NICMAC = m.nicMAC
		}
		if m.hostname != "" {
			opts.Hostname = m.hostname
		}
		if m.headless {
			v := true
			opts.Headless = &v
		}
		if m.uefi {
			v := true
			opts.UEFI = &v
		}
		if mode := gpuModeNames[m.gpuModeIdx]; mode != "" && mode != "none" {
			opts.GPUMode = mode
		}
		if m.gpuPCI != "" {
			opts.GPUPCI = m.gpuPCI
		}
		if vendor := gpuVendorNames[m.gpuVendorIdx]; vendor != "" {
			opts.GPUVendor = vendor
		}
		if m.antiDetect {
			v := true
			opts.AntiDetect = &v
		}
		if m.virtiofsDir != "" {
			opts.VirtiofsDir = m.virtiofsDir
		}
		if m.virtiofsTag != "" {
			opts.VirtiofsTag = m.virtiofsTag
		}
		if p := parseInt(m.spicePort); p > 0 {
			opts.SPICEPort = &p
		}
		if p := parseInt(m.sshPort); p > 0 {
			opts.SSHPort = p
		}
		if m.sshShare {
			v := true
			opts.SSHShare = &v
		}
		if m.sshKeyFile != "" {
			opts.SSHKeyFile = m.sshKeyFile
		}
	}
	return opts
}

func parseInt(s string) int {
	var n int
	if _, err := fmt.Sscan(strings.TrimSpace(s), &n); err != nil {
		return 0
	}
	return n
}
