package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Benehiko/vee/internal/backup"
)

var (
	styleBackupTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Padding(0, 1)
	styleBackupSelected = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	styleBackupCursor   = lipgloss.NewStyle().Background(lipgloss.Color("237"))
	styleBackupDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleBackupHelp     = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Padding(0, 1)
	styleBackupCheck    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleBackupFilter   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleBackupFilterPf = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
)

type dirNode struct {
	entry    *backup.DirEntry
	checked  bool
	expanded bool
	visible  bool
}

// BackupPickerModel is a standalone bubbletea model for selecting guest dirs.
// Run it with tea.NewProgram; when confirmed it quits and Result holds chosen paths.
type BackupPickerModel struct {
	nodes      []*dirNode
	cursor     int
	scrollOff  int    // index into visibleFiltered() of the top rendered row
	height     int    // terminal rows available for the list
	width      int    // terminal columns
	filter     string // current filter string
	filtering  bool   // true while user is typing a filter
	showHidden bool   // show dotfiles
	Result     []string
}

// NewBackupPicker returns a model populated with the given entries.
func NewBackupPicker(entries []*backup.DirEntry) BackupPickerModel {
	nodes := buildNodes(entries)
	return BackupPickerModel{nodes: nodes, height: 24}
}

func buildNodes(entries []*backup.DirEntry) []*dirNode {
	nodes := make([]*dirNode, len(entries))
	for i, e := range entries {
		nodes[i] = &dirNode{entry: e, visible: true}
	}
	// Initially collapse everything below depth 1 (home subdirs).
	homeDepth := 0
	if len(nodes) > 0 {
		homeDepth = nodes[0].entry.Depth
	}
	for _, n := range nodes {
		if n.entry.Depth > homeDepth+1 {
			n.visible = false
		}
	}
	return nodes
}

func (m BackupPickerModel) Init() tea.Cmd { return nil }

func (m BackupPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		// Reserve: 1 title + 1 blank + 1 status + 1 help + 1 filter line = 5
		m.height = max(msg.Height-5, 4)
		m.clampScroll()

	case tea.KeyMsg:
		if m.filtering {
			return m.updateFilter(msg)
		}
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit

		case "q":
			return m, tea.Quit

		case "up", "k":
			m.moveCursor(-1)

		case "down", "j":
			m.moveCursor(1)

		case "pgup":
			m.moveCursor(-m.height)

		case "pgdown":
			m.moveCursor(m.height)

		case "g":
			m.cursor = 0
			m.scrollOff = 0

		case "G":
			vis := m.visibleFiltered()
			m.cursor = len(vis) - 1
			m.clampScroll()

		case " ":
			m.toggleCheck()

		case "enter", "l":
			m.toggleExpand()

		case "right":
			m.expandAll()

		case "left":
			m.collapse()

		case "a":
			anyChecked := false
			for _, n := range m.visibleFiltered() {
				if n.checked {
					anyChecked = true
					break
				}
			}
			for _, n := range m.visibleFiltered() {
				n.checked = !anyChecked
			}

		case ".", "h":
			m.showHidden = !m.showHidden
			// Reset cursor to avoid pointing past the new list length.
			m.cursor = 0
			m.scrollOff = 0

		case "/":
			m.filtering = true
			m.filter = ""

		case "esc":
			m.filter = ""
			m.cursor = 0
			m.scrollOff = 0

		case "c":
			m.Result = m.selected()
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m BackupPickerModel) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "esc":
		m.filtering = false
		m.cursor = 0
		m.scrollOff = 0
	case "ctrl+c":
		return m, tea.Quit
	case "backspace", "ctrl+h":
		if len(m.filter) > 0 {
			m.filter = m.filter[:len(m.filter)-1]
		}
	default:
		if len(msg.Runes) == 1 {
			m.filter += string(msg.Runes)
		}
	}
	return m, nil
}

func (m *BackupPickerModel) moveCursor(delta int) {
	vis := m.visibleFiltered()
	if len(vis) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(vis) {
		m.cursor = len(vis) - 1
	}
	m.clampScroll()
}

func (m *BackupPickerModel) clampScroll() {
	vis := m.visibleFiltered()
	total := len(vis)
	if m.cursor < m.scrollOff {
		m.scrollOff = m.cursor
	}
	if m.cursor >= m.scrollOff+m.height {
		m.scrollOff = m.cursor - m.height + 1
	}
	if m.scrollOff > total-m.height {
		m.scrollOff = total - m.height
	}
	if m.scrollOff < 0 {
		m.scrollOff = 0
	}
}

func (m *BackupPickerModel) toggleCheck() {
	vis := m.visibleFiltered()
	if m.cursor >= len(vis) {
		return
	}
	vis[m.cursor].checked = !vis[m.cursor].checked
}

func (m *BackupPickerModel) expandAll() {
	vis := m.visibleFiltered()
	if m.cursor >= len(vis) {
		return
	}
	m.expandRecursive(vis[m.cursor])
}

func (m *BackupPickerModel) expandRecursive(node *dirNode) {
	if !hasChildren(m.nodes, node) {
		return
	}
	node.expanded = true
	// Make direct children visible and recurse into them.
	depth := node.entry.Depth
	inRange := false
	for _, n := range m.nodes {
		if n == node {
			inRange = true
			continue
		}
		if !inRange {
			continue
		}
		if n.entry.Depth <= depth {
			break
		}
		if n.entry.Depth == depth+1 {
			n.visible = true
			m.expandRecursive(n)
		}
	}
}

func (m *BackupPickerModel) collapse() {
	vis := m.visibleFiltered()
	if m.cursor >= len(vis) {
		return
	}
	node := vis[m.cursor]
	if node.expanded {
		node.expanded = false
		m.setChildrenVisible(node, false)
		// Children are gone; find the collapsed node in the updated list and
		// move the cursor there so it doesn't land on an unrelated entry.
		for i, n := range m.visibleFiltered() {
			if n == node {
				m.cursor = i
				break
			}
		}
		m.clampScroll()
	} else {
		// Already collapsed — jump to parent.
		m.jumpToParent(node)
	}
}

func (m *BackupPickerModel) setChildrenVisible(parent *dirNode, show bool) {
	depth := parent.entry.Depth
	inRange := false
	for _, n := range m.nodes {
		if n == parent {
			inRange = true
			continue
		}
		if !inRange {
			continue
		}
		if n.entry.Depth <= depth {
			break
		}
		if n.entry.Depth == depth+1 {
			n.visible = show
		} else if !show {
			n.visible = false
		}
	}
}

func (m *BackupPickerModel) jumpToParent(node *dirNode) {
	vis := m.visibleFiltered()
	depth := node.entry.Depth
	// Walk backward in the full node list from this node's position to find the parent.
	found := false
	for i := len(m.nodes) - 1; i >= 0; i-- {
		if m.nodes[i] == node {
			found = true
			continue
		}
		if !found {
			continue
		}
		if m.nodes[i].entry.Depth < depth {
			// Found parent — locate it in vis.
			for j, v := range vis {
				if v == m.nodes[i] {
					m.cursor = j
					m.clampScroll()
					return
				}
			}
			return
		}
	}
}

func (m *BackupPickerModel) toggleExpand() {
	if m.cursor >= len(m.visibleFiltered()) {
		return
	}
	node := m.visibleFiltered()[m.cursor]
	if node.expanded {
		node.expanded = false
		m.setChildrenVisible(node, false)
		for i, n := range m.visibleFiltered() {
			if n == node {
				m.cursor = i
				break
			}
		}
		m.clampScroll()
	} else {
		node.expanded = true
		m.setChildrenVisible(node, true)
	}
}

// visibleFiltered returns nodes that are visible, pass the hidden filter,
// and match the current filter string (or are ancestors of matching nodes).
// When a filter is active, results are ordered: prefix matches first, then
// middle/suffix matches, preserving original order within each group.
func (m *BackupPickerModel) visibleFiltered() []*dirNode {
	if m.filter == "" {
		var out []*dirNode
		for _, n := range m.nodes {
			if !n.visible {
				continue
			}
			if !m.showHidden && isDotfile(n.entry.Name) {
				continue
			}
			out = append(out, n)
		}
		return out
	}

	// Build set of paths that match directly, plus all their ancestor paths.
	lf := strings.ToLower(m.filter)
	ancestorOf := make(map[string]bool) // paths that are ancestors of a match
	directMatch := make(map[string]int) // path → matchPriority
	for _, n := range m.nodes {
		if !m.showHidden && isDotfile(n.entry.Name) {
			continue
		}
		lname := strings.ToLower(n.entry.Name)
		if idx := strings.Index(lname, lf); idx >= 0 {
			pri := 0
			if idx > 0 {
				pri = 1
			}
			directMatch[n.entry.Path] = pri
			// Mark all ancestor paths.
			p := n.entry.Path
			for {
				parent := p[:strings.LastIndex(p, "/")]
				if parent == "" || parent == p {
					break
				}
				ancestorOf[parent] = true
				p = parent
			}
		}
	}

	// Collect matching nodes in two passes: prefix matches then mid matches.
	// Ancestors of matches are included so tree context is visible.
	// Expanded nodes always show their visible children, even under a filter,
	// so that manually-expanded subtrees remain navigable.
	seen := make(map[string]bool)
	// expandedPaths tracks paths of included nodes that are expanded.
	expandedPaths := make(map[string]bool)
	var prefix, mid []*dirNode

	addNode := func(n *dirNode, pri int) {
		if seen[n.entry.Path] {
			return
		}
		seen[n.entry.Path] = true
		if n.expanded {
			expandedPaths[n.entry.Path] = true
		}
		if pri == 0 {
			prefix = append(prefix, n)
		} else {
			mid = append(mid, n)
		}
	}

	// isUnderExpanded returns true if any ancestor of n is in expandedPaths.
	isUnderExpanded := func(n *dirNode) bool {
		p := n.entry.Path
		for {
			slash := strings.LastIndex(p, "/")
			if slash <= 0 {
				break
			}
			p = p[:slash]
			if expandedPaths[p] {
				return true
			}
		}
		return false
	}

	for _, n := range m.nodes {
		if !n.visible {
			continue
		}
		if !m.showHidden && isDotfile(n.entry.Name) {
			continue
		}
		pri, direct := directMatch[n.entry.Path]
		isAnc := ancestorOf[n.entry.Path]
		switch {
		case direct:
			addNode(n, pri)
		case isAnc:
			addNode(n, 1)
		case isUnderExpanded(n):
			// Child of an expanded included node — show it in the same group as
			// its parent (mid), preserving tree order.
			addNode(n, 1)
		}
	}

	return append(prefix, mid...)
}

func isDotfile(name string) bool {
	return strings.HasPrefix(name, ".")
}

func (m *BackupPickerModel) selected() []string {
	var out []string
	for _, n := range m.nodes {
		if n.checked {
			out = append(out, n.entry.Path)
		}
	}
	return out
}

func (m BackupPickerModel) View() string {
	var b strings.Builder

	// Header
	b.WriteString(styleBackupTitle.Render("Select directories to back up") + "\n\n")

	vis := m.visibleFiltered()
	total := len(vis)
	base := minDepth(m.nodes)

	// Viewport slice
	end := min(m.scrollOff+m.height, total)

	for i := m.scrollOff; i < end; i++ {
		node := vis[i]
		indent := strings.Repeat("  ", node.entry.Depth-base)

		expand := " "
		if node.expanded {
			expand = "▾"
		} else if hasChildren(m.nodes, node) {
			expand = "▸"
		}

		check := "[ ]"
		if node.checked {
			check = styleBackupCheck.Render("[✓]")
		}

		name := node.entry.Name
		// Highlight filter match inside name.
		if m.filter != "" {
			name = highlightMatch(name, m.filter)
		}

		line := fmt.Sprintf("%s%s %s %s", indent, expand, check, name)

		switch {
		case i == m.cursor:
			line = styleBackupCursor.Render(line)
		case node.checked:
			line = styleBackupSelected.Render(line)
		default:
			line = styleBackupDim.Render(line)
		}

		b.WriteString(line + "\n")
	}

	// Scroll indicator
	sel := m.selected()
	b.WriteString("\n")

	hidden := ""
	if !m.showHidden {
		hidden = "  h show hidden"
	} else {
		hidden = "  h hide hidden"
	}

	statusLine := fmt.Sprintf("%d selected  %d/%d", len(sel), m.cursor+1, total)
	b.WriteString(styleBackupDim.Render(statusLine) + "\n")

	// Filter bar or help
	if m.filtering {
		b.WriteString(styleBackupFilterPf.Render("/") + styleBackupFilter.Render(m.filter+"█") + "\n")
		b.WriteString(styleBackupHelp.Render("enter/esc exit filter"))
	} else {
		filterHint := ""
		if m.filter != "" {
			filterHint = fmt.Sprintf("  filter:%q  esc clear", m.filter)
		}
		b.WriteString(styleBackupHelp.Render(
			"↑/↓/pgup/pgdn move  → expand all  ← collapse  space toggle  a all  / filter  c confirm  q quit" +
				hidden + filterHint,
		))
	}

	return b.String()
}

// highlightMatch wraps the matched substring in the filter style.
func highlightMatch(name, filter string) string {
	lower := strings.ToLower(name)
	idx := strings.Index(lower, strings.ToLower(filter))
	if idx < 0 {
		return name
	}
	return name[:idx] +
		styleBackupFilter.Render(name[idx:idx+len(filter)]) +
		name[idx+len(filter):]
}

func minDepth(nodes []*dirNode) int {
	if len(nodes) == 0 {
		return 0
	}
	d := nodes[0].entry.Depth
	for _, n := range nodes {
		if n.entry.Depth < d {
			d = n.entry.Depth
		}
	}
	return d
}

func hasChildren(nodes []*dirNode, target *dirNode) bool {
	found := false
	for _, n := range nodes {
		if n == target {
			found = true
			continue
		}
		if found && n.entry.Depth == target.entry.Depth+1 &&
			strings.HasPrefix(n.entry.Path, target.entry.Path+"/") {
			return true
		}
	}
	return false
}

// RunBackupPicker runs the dir-picker TUI and returns the selected paths.
func RunBackupPicker(entries []*backup.DirEntry) ([]string, error) {
	m := NewBackupPicker(entries)
	prog := tea.NewProgram(m, tea.WithAltScreen())
	final, err := prog.Run()
	if err != nil {
		return nil, err
	}
	result := final.(BackupPickerModel).Result
	return result, nil
}
