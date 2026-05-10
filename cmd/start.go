package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Benehiko/vee/internal/vm"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var startForeground bool

var startCmd = &cobra.Command{
	Use:               "start <name>",
	Short:             "Start a VM",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		mgr := vm.NewManager(prov)
		stdinReader := bufio.NewReader(os.Stdin)
		mgr.PromptFn = func(prompt string) (string, error) {
			fmt.Fprint(os.Stderr, prompt)
			if strings.Contains(strings.ToLower(prompt), "password") {
				pw, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
				return string(pw), err
			}
			line, err := stdinReader.ReadString('\n')
			return strings.TrimRight(line, "\r\n"), err
		}
		if err := mgr.Start(cmd.Context(), name, startForeground); err != nil {
			return err
		}
		if startForeground {
			return nil
		}
		return runStartSpinner(cmd, mgr, name)
	},
}

// runStartSpinner shows a bubbletea spinner while WaitReady polls for SSH/QGA.
// Falls back to plain text output when stdout is not a TTY.
func runStartSpinner(cmd *cobra.Command, mgr *vm.Manager, name string) error {
	ctx := cmd.Context()

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- mgr.WaitReady(ctx, name, 10*time.Minute)
	}()

	if !term.IsTerminal(int(os.Stdout.Fd())) {
		err := <-doneCh
		if err != nil {
			return fmt.Errorf("wait ready: %w", err)
		}
		fmt.Printf("VM %q is ready\n", name)
		return nil
	}

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	m := &startSpinnerModel{
		spinner: s,
		name:    name,
		status:  "starting…",
	}

	p := tea.NewProgram(m)

	go func() {
		elapsed := 0
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case err := <-doneCh:
				p.Send(startDoneMsg{err: err})
				return
			case <-tick.C:
				elapsed++
				secs := elapsed / 2
				p.Send(startStatusMsg(fmt.Sprintf("waiting for VM to become ready… %ds", secs)))
			}
		}
	}()

	result, err := p.Run()
	if err != nil {
		return err
	}
	final := result.(*startSpinnerModel)
	if final.err != nil {
		return fmt.Errorf("wait ready: %w", final.err)
	}
	fmt.Printf("VM %q is ready\n", name)
	return nil
}

type (
	startStatusMsg string
	startDoneMsg   struct{ err error }
)

type startSpinnerModel struct {
	spinner spinner.Model
	name    string
	status  string
	done    bool
	err     error
}

func (m *startSpinnerModel) Init() tea.Cmd { return m.spinner.Tick }

func (m *startSpinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case startDoneMsg:
		m.done = true
		m.err = msg.err
		return m, tea.Quit
	case startStatusMsg:
		m.status = string(msg)
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			m.err = fmt.Errorf("interrupted")
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
}

func (m *startSpinnerModel) View() string {
	if m.done {
		return ""
	}
	return m.spinner.View() + " " + m.status + "\n"
}

func init() {
	startCmd.Flags().BoolVar(&startForeground, "foreground", false, "Run in foreground (block until VM exits)")
}
