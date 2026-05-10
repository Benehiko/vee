package tui

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Benehiko/vee/internal/backup"
)

var (
	styleLoaderSpinner = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	styleLoaderDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleLoaderStale   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

// dirEntryMsg carries a single entry streamed from SSH.
type dirEntryMsg struct{ entry *backup.DirEntry }

// enumDoneMsg signals that streaming is complete.
type enumDoneMsg struct{ err error }

// BackupLoaderModel shows a spinner while enumerating guest dirs.
// When enumeration is complete it transitions to BackupPickerModel.
// Final result is in Result after the program exits.
type BackupLoaderModel struct {
	spinner spinner.Model
	conn    backup.SSHConn
	db      *sql.DB
	vmName  string
	entries []*backup.DirEntry // accumulates streaming results
	ch      <-chan *backup.DirEntry
	errCh   <-chan error
	stale   bool // true when showing cached data while refreshing
	done    bool // enumeration complete
	err     error
	width   int
	height  int
	Result  []string
	picker  *BackupPickerModel
}

// NewBackupLoader creates the loader. If db is non-nil and a fresh cache exists,
// the picker opens immediately with cached data and a background refresh starts.
func NewBackupLoader(conn backup.SSHConn, db *sql.DB, vmName string) BackupLoaderModel {
	s := spinner.New()
	s.Spinner = spinner.MiniDot
	s.Style = styleLoaderSpinner
	return BackupLoaderModel{
		spinner: s,
		conn:    conn,
		db:      db,
		vmName:  vmName,
		height:  24,
	}
}

func (m BackupLoaderModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.startEnum())
}

// startEnum checks the cache and starts streaming.
func (m BackupLoaderModel) startEnum() tea.Cmd {
	return func() tea.Msg {
		// Try fresh cache first.
		if m.db != nil {
			if cached, err := backup.CacheGet(m.db, m.vmName); err == nil && len(cached) > 0 {
				// Cache hit — return all entries at once, then kick off background refresh.
				return cachedEntriesMsg{entries: cached}
			}
		}
		// No cache — start streaming.
		return startStreamMsg{}
	}
}

type (
	cachedEntriesMsg struct{ entries []*backup.DirEntry }
	startStreamMsg   struct{}
)

// streamNextCmd reads one entry from ch (shared across calls) and returns it as
// a dirEntryMsg, or enumDoneMsg when the channel is closed.
func streamNextCmd(ch <-chan *backup.DirEntry, errCh <-chan error) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			var err error
			select {
			case err = <-errCh:
			default:
			}
			return enumDoneMsg{err: err}
		}
		return dirEntryMsg{entry: e}
	}
}

func startStreamCmd(conn backup.SSHConn) tea.Cmd {
	return func() tea.Msg {
		ch := make(chan *backup.DirEntry, 64)
		errCh := make(chan error, 1)
		go backup.EnumerateHomeStream(conn, ch, errCh)
		return streamReadyMsg{ch: ch, errCh: errCh}
	}
}

type streamReadyMsg struct {
	ch    <-chan *backup.DirEntry
	errCh <-chan error
}

// backgroundRefreshCmd re-enumerates and updates the cache without blocking the UI.
func backgroundRefreshCmd(conn backup.SSHConn, db *sql.DB, vmName string) tea.Cmd {
	return func() tea.Msg {
		ch := make(chan *backup.DirEntry, 64)
		errCh := make(chan error, 1)
		go backup.EnumerateHomeStream(conn, ch, errCh)
		var entries []*backup.DirEntry
		for e := range ch {
			entries = append(entries, e)
		}
		select {
		case <-errCh:
			return nil // best-effort; ignore refresh errors
		default:
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Path < entries[j].Path
		})
		if db != nil && len(entries) > 0 {
			_ = backup.CacheSet(db, vmName, entries)
		}
		return refreshDoneMsg{entries: entries}
	}
}

type refreshDoneMsg struct{ entries []*backup.DirEntry }

func (m BackupLoaderModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// If picker is active, delegate to it.
	if m.picker != nil {
		switch msg := msg.(type) {
		case tea.WindowSizeMsg:
			m.width, m.height = msg.Width, msg.Height
		case refreshDoneMsg:
			// Silently update cache; picker stays as-is.
			return m, nil
		}
		next, cmd := m.picker.Update(msg)
		p := next.(BackupPickerModel)
		m.picker = &p
		if p.Result != nil {
			m.Result = p.Result
			return m, tea.Quit
		}
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case cachedEntriesMsg:
		// Show picker immediately with cached data; refresh in background.
		picker := NewBackupPicker(msg.entries)
		picker.width = m.width
		picker.height = max(m.height-5, 4)
		m.picker = &picker
		m.stale = true
		return m, tea.Batch(
			backgroundRefreshCmd(m.conn, m.db, m.vmName),
			m.picker.Init(),
		)

	case startStreamMsg:
		return m, startStreamCmd(m.conn)

	case streamReadyMsg:
		m.ch = msg.ch
		m.errCh = msg.errCh
		return m, streamNextCmd(m.ch, m.errCh)

	case dirEntryMsg:
		m.entries = append(m.entries, msg.entry)
		return m, streamNextCmd(m.ch, m.errCh)

	case enumDoneMsg:
		m.done = true
		m.err = msg.err
		if msg.err == nil && len(m.entries) > 0 {
			sort.Slice(m.entries, func(i, j int) bool {
				return m.entries[i].Path < m.entries[j].Path
			})
			if m.db != nil {
				_ = backup.CacheSet(m.db, m.vmName, m.entries)
			}
			picker := NewBackupPicker(m.entries)
			picker.width = m.width
			picker.height = max(m.height-5, 4)
			m.picker = &picker
			return m, m.picker.Init()
		}
		return m, tea.Quit

	case refreshDoneMsg:
		// Background refresh done while in loading state (shouldn't normally happen).
		return m, nil
	}
	return m, nil
}

func (m BackupLoaderModel) View() string {
	if m.picker != nil {
		v := m.picker.View()
		if m.stale {
			v += "\n" + styleLoaderStale.Render("  ↻ refreshing directory list in background…")
		}
		return v
	}
	if m.err != nil {
		return fmt.Sprintf("Error enumerating directories: %v\n", m.err)
	}

	var sb strings.Builder
	sb.WriteString(styleBackupTitle.Render("Select directories to back up") + "\n\n")
	sb.WriteString(m.spinner.View() + " ")
	sb.WriteString(styleLoaderDim.Render(fmt.Sprintf("Enumerating guest directories… (%d found)", len(m.entries))))
	sb.WriteString("\n")
	return sb.String()
}

// RunBackupLoader runs the combined loader+picker TUI and returns selected paths.
func RunBackupLoader(conn backup.SSHConn, db *sql.DB, vmName string) ([]string, error) {
	m := NewBackupLoader(conn, db, vmName)
	prog := tea.NewProgram(m, tea.WithAltScreen())
	final, err := prog.Run()
	if err != nil {
		return nil, err
	}
	return final.(BackupLoaderModel).Result, nil
}
