// Package tui provides the interactive terminal UI launched by the bare "vee" command.
// It contains three screens: list (default), create form, and per-VM monitor.
package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

// screen identifies which view is active.
type screen int

const (
	screenList screen = iota
	screenCreate
	screenMonitor
)

// app is the root bubbletea model that owns the active screen.
type app struct {
	prov    provider.Provider
	mgr     *vm.Manager
	active  screen
	list    listModel
	create  createModel
	monitor monitorModel
	width   int
	height  int
}

func newApp(p provider.Provider) app {
	mgr := vm.NewManager(p)
	return app{
		prov:   p,
		mgr:    mgr,
		active: screenList,
		list:   newListModel(mgr),
		create: newCreateModel(mgr, p),
	}
}

func (a app) Init() tea.Cmd {
	return a.list.Init()
}

func (a app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height

	case switchScreen:
		a.active = msg.to
		switch msg.to {
		case screenList:
			a.list = newListModel(a.mgr)
			return a, a.list.Init()
		case screenCreate:
			a.create = newCreateModel(a.mgr, a.prov)
			return a, a.create.Init()
		case screenMonitor:
			a.monitor = newMonitorModel(msg.vmName, msg.qmpSocket)
			return a, a.monitor.Init()
		}
	}

	switch a.active {
	case screenList:
		next, cmd := a.list.Update(msg)
		a.list = next.(listModel)
		return a, cmd
	case screenCreate:
		next, cmd := a.create.Update(msg)
		a.create = next.(createModel)
		return a, cmd
	case screenMonitor:
		next, cmd := a.monitor.Update(msg)
		a.monitor = next.(monitorModel)
		return a, cmd
	}
	return a, nil
}

func (a app) View() string {
	switch a.active {
	case screenList:
		return a.list.View()
	case screenCreate:
		return a.create.View()
	case screenMonitor:
		return a.monitor.View()
	}
	return ""
}

// switchScreen is sent to transition between views.
type switchScreen struct {
	to        screen
	vmName    string
	qmpSocket string
}

func gotoList() tea.Cmd {
	return func() tea.Msg { return switchScreen{to: screenList} }
}

func gotoCreate() tea.Cmd {
	return func() tea.Msg { return switchScreen{to: screenCreate} }
}

func gotoMonitor(name, qmpSocket string) tea.Cmd {
	return func() tea.Msg {
		return switchScreen{to: screenMonitor, vmName: name, qmpSocket: qmpSocket}
	}
}

// Run launches the TUI, blocking until the user quits.
func Run(ctx context.Context, p provider.Provider) error {
	a := newApp(p)
	prog := tea.NewProgram(a, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := prog.Run()
	if err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	return nil
}
