package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	styleDirTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Padding(0, 1)
	styleDirCursor = lipgloss.NewStyle().Background(lipgloss.Color("237"))
	styleDirDir    = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	styleDirFaint  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleDirHelp   = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Padding(0, 1)
	styleDirSel    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
)

// hostDirPickerModel browses the host filesystem.
// Embed it in the parent model; call handleKey and View directly.
// esc/q signals cancel (caller sets dirPicker = nil).
// space/c signals confirm (caller reads cwd and sets dirPicker = nil).
type hostDirPickerModel struct {
	cwd        string
	entries    []os.DirEntry // dirs in cwd
	cursor     int
	scrollOff  int
	height     int
	showHidden bool
}

func newHostDirPicker(startDir string) hostDirPickerModel {
	if startDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			startDir = "/"
		} else {
			startDir = home
		}
	}
	m := hostDirPickerModel{height: 20}
	m.cd(startDir)
	return m
}

func (m *hostDirPickerModel) cd(dir string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return
	}
	var dirs []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !m.showHidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dirs = append(dirs, e)
	}
	m.cwd = abs
	m.entries = dirs
	m.cursor = 0
	m.scrollOff = 0
}

// handleKey processes a key string. Returns true if the key was consumed.
// esc/q and space/c are handled by the parent before calling handleKey.
func (m *hostDirPickerModel) handleKey(key string) {
	switch key {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.clampScroll()
		}

	case "down", "j":
		if m.cursor < len(m.entries)-1 {
			m.cursor++
			m.clampScroll()
		}

	case "pgup":
		m.cursor -= m.height
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.clampScroll()

	case "pgdown":
		m.cursor += m.height
		if m.cursor >= len(m.entries) {
			m.cursor = len(m.entries) - 1
		}
		m.clampScroll()

	case "right", "enter", "l":
		if len(m.entries) > 0 {
			m.cd(filepath.Join(m.cwd, m.entries[m.cursor].Name()))
		}

	case "left", "h":
		m.cd(filepath.Dir(m.cwd))

	case ".":
		m.showHidden = !m.showHidden
		m.cd(m.cwd)
	}
}

func (m *hostDirPickerModel) clampScroll() {
	if m.cursor < m.scrollOff {
		m.scrollOff = m.cursor
	}
	if m.cursor >= m.scrollOff+m.height {
		m.scrollOff = m.cursor - m.height + 1
	}
	if m.scrollOff < 0 {
		m.scrollOff = 0
	}
}

func (m hostDirPickerModel) View() string {
	var b strings.Builder

	b.WriteString(styleDirTitle.Render("Select host directory") + "\n")
	b.WriteString(styleDirSel.Render("  "+m.cwd) + "\n\n")

	end := m.scrollOff + m.height
	if end > len(m.entries) {
		end = len(m.entries)
	}

	if len(m.entries) == 0 {
		b.WriteString(styleDirFaint.Render("  (empty)") + "\n")
	}

	for i := m.scrollOff; i < end; i++ {
		name := m.entries[i].Name() + "/"
		line := fmt.Sprintf("  %s", name)
		if i == m.cursor {
			line = styleDirCursor.Render(line)
		} else {
			line = styleDirDir.Render(line)
		}
		b.WriteString(line + "\n")
	}

	b.WriteString("\n")
	b.WriteString(styleDirHelp.Render(
		"↑/↓ move  → enter dir  ← parent  space/c confirm  . toggle hidden  esc cancel",
	))
	return b.String()
}
