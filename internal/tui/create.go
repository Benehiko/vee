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
	fieldName     createField = iota
	fieldTemplate             // hidden when noAutoInstall
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
	fieldNICBridge // visible only when bridge mode
	fieldHostname
	fieldHeadless
	fieldUEFI
	fieldGPUMode
	fieldGPUPCI     // visible only when passthrough
	fieldGPUVendor  // hidden when none or passthrough
	fieldAntiDetect // visible only when passthrough
	fieldVirtiofsDir
	fieldVirtiofsTag
	fieldDataDisks    // repeatable block device passthrough
	fieldDataDiskBoot // visible only when a data disk is selected
	fieldSSHKeyFile   // import existing SSH public keys file
	fieldUser
	fieldPassword
	fieldCreate // [ Create ] submit button

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
	dirPicker    *hostDirPickerModel // non-nil while the dir browser overlay is active

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
	nicModeIdx    int
	nicBridge     string
	hostname      string
	headless      bool
	uefi          bool
	gpuModeIdx    int
	gpuPCI        string
	gpuVendorIdx  int
	antiDetect    bool
	virtiofsDir   string
	virtiofsTag   string
	dataDevs      []blockdev.Device // block devices available for data-disk passthrough
	dataDevIdx    int               // 0 = none, 1..N = dataDevs[dataDevIdx-1]
	dataDiskBoot  bool              // mark the selected data disk as UEFI boot priority 1
	sshKeyFile    string            // path to file of extra SSH public keys to import
	user          string
	password      string
	noAutoInstall bool // boot from existing disk, skip install pass; hides Template

	err            string
	submitting     bool
	confirmPending bool // true while the "are you sure?" popup is shown
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
		dataDevs:    devs,
		virtiofsTag: "share",
		user:        "vee",
		password:    "vee",
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
	if o.SSHKeyFile != "" {
		m.sshKeyFile = o.SSHKeyFile
		m.advancedOpen = true
	}
	if o.User != "" {
		m.user = o.User
	}
	if o.Password != "" {
		m.password = o.Password
	}
	if o.NoAutoInstall {
		m.noAutoInstall = true
		// Existing disk: clear the default user/password pre-fill.
		m.user = ""
		m.password = ""
	}
	if len(o.DataDisks) > 0 {
		m.advancedOpen = true
	}
	if o.BootDisk != "" {
		m.advancedOpen = true
		for i, d := range m.dataDevs {
			if d.ByIDPath == o.BootDisk {
				m.dataDevIdx = i + 1
				m.dataDiskBoot = true
				break
			}
		}
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
	case fieldTemplate:
		return !m.noAutoInstall
	case fieldDistro, fieldDistroVersion:
		return m.isDistroAware() && !m.noAutoInstall
	case fieldDiskSize:
		return m.dMode == diskModeNew
	case fieldDiskDev:
		return m.dMode == diskModePassthrough
	case fieldAdvancedToggle:
		return true
	case fieldNICBridge:
		return m.advancedOpen && nicModeNames[m.nicModeIdx] == "bridge"
	case fieldGPUPCI:
		return m.advancedOpen && gpuModeNames[m.gpuModeIdx] == "passthrough"
	case fieldGPUVendor:
		// Vendor only matters for virtio mode; passthrough uses the physical card.
		return m.advancedOpen && gpuModeNames[m.gpuModeIdx] == "virtio"
	case fieldAntiDetect:
		return m.advancedOpen && gpuModeNames[m.gpuModeIdx] == "passthrough"
	case fieldDataDiskBoot:
		return m.advancedOpen && m.dataDevIdx > 0
	case fieldUser, fieldPassword:
		// Only meaningful for new VMs (auto-install injects cloud-init).
		return !m.noAutoInstall
	}
	if f >= fieldNICMode {
		return m.advancedOpen
	}
	return true
}

func (m createModel) Init() tea.Cmd { return nil }

func (m createModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Confirm popup — only y/n/esc are meaningful.
	if m.confirmPending {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "y", "Y":
				m.confirmPending = false
				m.submitting = true
				return m, m.doSubmit()
			case "n", "N", "esc":
				m.confirmPending = false
			}
		}
		return m, nil
	}

	// Dir picker overlay — route all input to the picker while it's open.
	if m.dirPicker != nil {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "esc", "q":
				m.dirPicker = nil
				return m, nil
			case " ", "c":
				m.virtiofsDir = m.dirPicker.cwd
				m.dirPicker = nil
				return m, nil
			default:
				m.dirPicker.handleKey(key.String())
				return m, nil
			}
		}
		if ws, ok := msg.(tea.WindowSizeMsg); ok {
			if ws.Height > 4 {
				m.dirPicker.height = ws.Height - 4
			}
		}
		return m, nil
	}

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
		case "C":
			m.confirmPending = true
			return m, nil
		case "enter":
			if m.field == fieldAdvancedToggle {
				m.advancedOpen = !m.advancedOpen
				return m, nil
			}
			if m.field == fieldVirtiofsDir {
				p := newHostDirPicker(m.virtiofsDir)
				m.dirPicker = &p
				return m, nil
			}
			if m.field == fieldCreate {
				m.confirmPending = true
				return m, nil
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
			case fieldDataDiskBoot:
				m.dataDiskBoot = !m.dataDiskBoot
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
	case fieldHostname:
		m.hostname += ch
	case fieldGPUPCI:
		m.gpuPCI += ch
	case fieldVirtiofsDir:
		m.virtiofsDir += ch
	case fieldVirtiofsTag:
		m.virtiofsTag += ch
	case fieldSSHKeyFile:
		m.sshKeyFile += ch
	case fieldUser:
		m.user += ch
	case fieldPassword:
		m.password += ch
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
	case fieldHostname:
		m.hostname = trim(m.hostname)
	case fieldGPUPCI:
		m.gpuPCI = trim(m.gpuPCI)
	case fieldVirtiofsDir:
		m.virtiofsDir = trim(m.virtiofsDir)
	case fieldVirtiofsTag:
		m.virtiofsTag = trim(m.virtiofsTag)
	case fieldSSHKeyFile:
		m.sshKeyFile = trim(m.sshKeyFile)
	case fieldUser:
		m.user = trim(m.user)
	case fieldPassword:
		m.password = trim(m.password)
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
	case fieldDataDisks:
		// 0 = none; 1..N = dataDevs[idx-1]
		m.dataDevIdx = clampIdx(m.dataDevIdx+delta, len(m.dataDevs)+1)
		if m.dataDevIdx == 0 {
			m.dataDiskBoot = false
		}
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

type fieldDef struct {
	label string
	value string
	f     createField
	skip  bool
}

func (m createModel) View() string {
	if m.dirPicker != nil {
		return m.dirPicker.View()
	}
	if m.confirmPending {
		return styleFormTitle.Render("  Create VM  ") + "\n\n" +
			"  Create VM " + styleFieldFocus.Render(m.name) + "?\n\n" +
			"  " + styleFieldValue.Render("[ y ]") + "  yes\n" +
			"  " + styleFieldValue.Render("[ n ]") + "  no\n\n" +
			styleFormHelp.Render("y confirm  n/esc cancel")
	}

	var sb strings.Builder

	sb.WriteString(styleFormTitle.Render("  Create VM  "))
	sb.WriteString("\n\n")

	basics := []fieldDef{
		{"Name", m.name + cursor(m.field == fieldName), fieldName, false},
		{"Template", templateSelector(m.tmplIdx, m.field == fieldTemplate), fieldTemplate, m.noAutoInstall},
		{"Distro", distroSelector(m.distroIdx, m.field == fieldDistro), fieldDistro, !m.isDistroAware() || m.noAutoInstall},
		{"Version", versionSelector(m.selectedDistro(), m.distroVerIdx, m.field == fieldDistroVersion), fieldDistroVersion, !m.isDistroAware() || m.noAutoInstall},
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
		nicBridgePlaceholder := m.nicBridge
		if nicBridgePlaceholder == "" && nicModeNames[m.nicModeIdx] == "bridge" {
			nicBridgePlaceholder = styleFaint.Render("br0")
		}
		advanced := []fieldDef{
			{"NIC Mode", selectorNamed(nicModeNames, m.nicModeIdx, m.field == fieldNICMode), fieldNICMode, false},
			{"NIC Bridge", nicBridgePlaceholder + cursor(m.field == fieldNICBridge), fieldNICBridge, nicModeNames[m.nicModeIdx] != "bridge"},
			{"Hostname", m.hostname + cursor(m.field == fieldHostname), fieldHostname, false},
			{"Headless", boolValue(m.headless, m.field == fieldHeadless), fieldHeadless, false},
			{"UEFI", boolValue(m.uefi, m.field == fieldUEFI), fieldUEFI, false},
			{"GPU Mode", selectorNamed(gpuModeNames, m.gpuModeIdx, m.field == fieldGPUMode), fieldGPUMode, false},
			{"GPU PCI", m.gpuPCI + cursor(m.field == fieldGPUPCI), fieldGPUPCI, gpuModeNames[m.gpuModeIdx] != "passthrough"},
			{"GPU Vendor", selectorNamed(gpuVendorNames, m.gpuVendorIdx, m.field == fieldGPUVendor), fieldGPUVendor, gpuModeNames[m.gpuModeIdx] != "virtio"},
			{"Anti-detect", boolValue(m.antiDetect, m.field == fieldAntiDetect), fieldAntiDetect, gpuModeNames[m.gpuModeIdx] != "passthrough"},
			{"Virtiofs Dir", virtiofsDirValue(m.virtiofsDir, m.field == fieldVirtiofsDir), fieldVirtiofsDir, false},
			{"Virtiofs Tag", m.virtiofsTag + cursor(m.field == fieldVirtiofsTag), fieldVirtiofsTag, false},
			{"Data Disk", dataDiskSelector(m.dataDevs, m.dataDevIdx, m.field == fieldDataDisks), fieldDataDisks, false},
			{"  Boot Disk", boolValue(m.dataDiskBoot, m.field == fieldDataDiskBoot), fieldDataDiskBoot, m.dataDevIdx == 0},
			{"Import SSH Keys", m.sshKeyFile + cursor(m.field == fieldSSHKeyFile), fieldSSHKeyFile, false},
			{"User", m.user + cursor(m.field == fieldUser), fieldUser, m.noAutoInstall},
			{"Password", maskPassword(m.password) + cursor(m.field == fieldPassword), fieldPassword, m.noAutoInstall},
		}
		m.renderFields(&sb, advanced)
	}

	sb.WriteString("\n")

	// [ Create ] button.
	if !m.submitting {
		btn := "[ Create ]"
		if m.field == fieldCreate {
			sb.WriteString("  " + styleFieldFocus.Render(btn) + "\n")
		} else {
			sb.WriteString("  " + styleFieldValue.Render(btn) + "\n")
		}
	}

	sb.WriteString("\n")
	if m.err != "" {
		sb.WriteString(styleFormErr.Render("  Error: "+m.err) + "\n\n")
	}
	if m.submitting {
		sb.WriteString(styleFieldFocus.Render("  Creating…") + "\n")
	}

	sb.WriteString(styleFormHelp.Render("tab/↑↓ navigate  ←/→ choose option  space toggle  C create  esc cancel"))
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

// dataDiskSelector renders a selector where index 0 = "none" and 1..N select a device.
func dataDiskSelector(devs []blockdev.Device, idx int, focused bool) string {
	var parts []string
	// "none" option at index 0.
	if idx == 0 {
		if focused {
			parts = append(parts, styleFieldFocus.Render("[ none ]"))
		} else {
			parts = append(parts, styleFieldValue.Render("[ none ]"))
		}
	} else {
		parts = append(parts, styleFaint.Render("none"))
	}
	for i, d := range devs {
		label := d.Label()
		if i+1 == idx {
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
		// Primary disk passthrough.
		if m.dMode == diskModePassthrough && len(m.blockDevs) > 0 {
			cfg.Disks = append(cfg.Disks, vm.DiskConfig{
				Path:        m.blockDevs[m.blockDevIdx].ByIDPath,
				Passthrough: true,
			})
		}
		// Data disk passthrough: index 0 = none, 1..N = dataDevs[idx-1].
		if m.advancedOpen && m.dataDevIdx > 0 && m.dataDevIdx-1 < len(m.dataDevs) {
			dev := m.dataDevs[m.dataDevIdx-1]
			bootIndex := 0
			if m.dataDiskBoot {
				bootIndex = 1
			}
			cfg.Disks = append(cfg.Disks, vm.DiskConfig{
				Path:        dev.ByIDPath,
				Passthrough: true,
				BootIndex:   bootIndex,
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
	opts.NoAutoInstall = m.noAutoInstall
	// Advanced.
	if m.advancedOpen {
		if mode := nicModeNames[m.nicModeIdx]; mode != "" {
			opts.NICMode = mode
		}
		if m.nicBridge != "" {
			opts.NICBridge = m.nicBridge
		} else if nicModeNames[m.nicModeIdx] == "bridge" {
			opts.NICBridge = "br0"
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
		if gpuModeNames[m.gpuModeIdx] == "virtio" {
			if vendor := gpuVendorNames[m.gpuVendorIdx]; vendor != "" {
				opts.GPUVendor = vendor
			}
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
		if m.sshKeyFile != "" {
			opts.SSHKeyFile = m.sshKeyFile
		}
	}
	if !m.noAutoInstall {
		if m.user != "" {
			opts.User = m.user
		}
		if m.password != "" {
			opts.Password = m.password
		}
	}
	return opts
}

func maskPassword(s string) string {
	if s == "" {
		return ""
	}
	return strings.Repeat("•", len(s))
}

func parseInt(s string) int {
	var n int
	if _, err := fmt.Sscan(strings.TrimSpace(s), &n); err != nil {
		return 0
	}
	return n
}

// virtiofsDirValue renders the Virtiofs Dir field. When focused it shows a
// "browse" hint; when a path is set it shows the path.
func virtiofsDirValue(dir string, focused bool) string {
	if dir != "" {
		return dir + cursor(focused)
	}
	if focused {
		return styleFieldFocus.Render("[ browse ]")
	}
	return styleFaint.Render("(enter to browse)")
}
