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
	nodes  []*dirNode
	cursor int
	Result []string
	done   bool
}

// NewBackupPicker returns a model that will load dirs via the provided fetch func.
func NewBackupPicker(entries []*backup.DirEntry) BackupPickerModel {
	nodes := buildNodes(entries)
	return BackupPickerModel{nodes: nodes}
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
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.done = true
			return m, tea.Quit

		case "up", "k":
			m.moveCursor(-1)

		case "down", "j":
			m.moveCursor(1)

		case " ":
			m.toggleCheck()

		case "enter", "right", "l":
			m.toggleExpand()

		case "a":
			// Select/deselect all visible.
			anyChecked := false
			for _, n := range m.visible() {
				if n.checked {
					anyChecked = true
					break
				}
			}
			for _, n := range m.visible() {
				n.checked = !anyChecked
			}

		case "c":
			// Confirm selection.
			m.Result = m.selected()
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *BackupPickerModel) moveCursor(delta int) {
	vis := m.visible()
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
}

func (m *BackupPickerModel) toggleCheck() {
	vis := m.visible()
	if m.cursor >= len(vis) {
		return
	}
	vis[m.cursor].checked = !vis[m.cursor].checked
}

func (m *BackupPickerModel) toggleExpand() {
	vis := m.visible()
	if m.cursor >= len(vis) {
		return
	}
	node := vis[m.cursor]
	node.expanded = !node.expanded
	depth := node.entry.Depth

	// Show/hide direct children.
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
			n.visible = node.expanded
		} else if !node.expanded {
			n.visible = false
		}
	}
}

func (m *BackupPickerModel) visible() []*dirNode {
	var out []*dirNode
	for _, n := range m.nodes {
		if n.visible {
			out = append(out, n)
		}
	}
	return out
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
	b.WriteString(styleBackupTitle.Render("Select directories to back up") + "\n\n")

	vis := m.visible()
	for i, node := range vis {
		indent := strings.Repeat("  ", node.entry.Depth-minDepth(m.nodes))

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

		line := fmt.Sprintf("%s%s %s %s", indent, expand, check, node.entry.Name)

		if i == m.cursor {
			line = styleBackupCursor.Render(line)
		} else if node.checked {
			line = styleBackupSelected.Render(line)
		} else {
			line = styleBackupDim.Render(line)
		}

		b.WriteString(line + "\n")
	}

	sel := m.selected()
	b.WriteString("\n")
	b.WriteString(styleBackupDim.Render(fmt.Sprintf("%d selected", len(sel))) + "\n")
	b.WriteString(styleBackupHelp.Render("↑/↓ move  space toggle  enter expand  a all  c confirm  q quit"))
	return b.String()
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
