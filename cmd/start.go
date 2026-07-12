package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Benehiko/vee/internal/boot"
	"github.com/Benehiko/vee/internal/journal"
	"github.com/Benehiko/vee/internal/qemubin"
	"github.com/Benehiko/vee/internal/vm"
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
		// Ensure the vee-managed QEMU binary is present and up-to-date.
		if qemuPath, err := qemubin.Ensure(); err != nil {
			return fmt.Errorf("qemu binary: %w", err)
		} else {
			prov.Config().QemuBinaryPath = qemuPath
		}

		wasInstalling := isInstalling(mgr, name)
		if startForeground {
			// Stream serial log + phase spinner in parallel while the install
			// pass runs. The serial streamer is cancelled once Start returns.
			serialCtx, cancelSerial := context.WithCancel(cmd.Context())
			serialPath := filepath.Join(prov.Config().StoragePath, name, "serial.log")
			go streamSerialForeground(serialCtx, serialPath)
			err := mgr.Start(cmd.Context(), name, true)
			cancelSerial()
			return err
		}
		if err := mgr.Start(cmd.Context(), name, false); err != nil {
			return err
		}
		// If the VM powered off immediately (install pass complete), skip the
		// readiness spinner — there is nothing to wait for.
		if installPassDone(mgr, name, wasInstalling) {
			fmt.Printf("Install complete. Run 'vee start %s' to boot.\n", name)
			return nil
		}
		maybeStartJournalListener(cmd, name)
		return runStartSpinner(cmd, mgr, name)
	},
}

// runStartSpinner shows a bubbletea spinner while WaitReadyWithPhases polls
// for SSH/QGA readiness and a serial-log watcher reports boot phase
// transitions. Falls back to plain text output when stdout is not a TTY.
func runStartSpinner(cmd *cobra.Command, mgr *vm.Manager, name string) error {
	ctx := cmd.Context()

	phaseCh, errCh := mgr.WaitReadyWithPhases(ctx, name, 10*time.Minute)

	//nolint:gosec // os.Stdout.Fd() is a small OS file descriptor; no overflow.
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		// Drain phase events to keep the watcher goroutine moving but render
		// nothing — we only print final status in non-TTY mode.
		go func() {
			for range phaseCh {
			}
		}()
		err := <-errCh
		if err != nil {
			return err
		}
		fmt.Printf("VM %q is ready\n", name)
		printTemplateHints(mgr, name)
		return nil
	}

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	m := &startSpinnerModel{
		spinner:    s,
		name:       name,
		status:     "starting…",
		phaseStart: time.Now(),
	}

	p := tea.NewProgram(m)

	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case err := <-errCh:
				p.Send(startDoneMsg{err: err})
				return
			case ev, ok := <-phaseCh:
				if !ok {
					phaseCh = nil
					continue
				}
				p.Send(startPhaseMsg{phase: ev.Phase, detail: ev.Detail, at: ev.At})
			case <-tick.C:
				p.Send(startTickMsg{})
			}
		}
	}()

	result, err := p.Run()
	if err != nil {
		return err
	}
	final := result.(*startSpinnerModel)
	if final.err != nil {
		return final.err
	}
	fmt.Printf("VM %q is ready\n", name)
	printTemplateHints(mgr, name)
	return nil
}

// printTemplateHints prints actionable next-step hints for specific templates.
func printTemplateHints(mgr *vm.Manager, name string) {
	cfg, err := mgr.LoadConfig(name)
	if err != nil {
		return
	}
	switch cfg.Template {
	case "docker":
		fmt.Printf("\nDocker daemon is ready. Point your client at this VM:\n")
		fmt.Printf("  export DOCKER_HOST=tcp://localhost:2375\n")
		fmt.Printf("  # fish:  set -x DOCKER_HOST tcp://localhost:2375\n")
	case "gaming-arch":
		fmt.Printf("\nGuest journal forwarding: vee logs %s --journal -f\n", name)
	}
}

// maybeStartJournalListener starts a systemd-journal-remote listener in the
// background for templates that forward guest journals (gaming-arch).
// The listener runs until ctx is cancelled; errors are logged but not fatal.
func maybeStartJournalListener(cmd *cobra.Command, name string) {
	mgr := vm.NewManager(prov)
	cfg, err := mgr.LoadConfig(name)
	if err != nil || cfg.Template != "gaming-arch" {
		return
	}

	dir := filepath.Join(prov.Config().StoragePath, name, "journal")
	port, err := journal.FreePort()
	if err != nil {
		prov.Logger().Sugar().Warnf("journal listener: %v", err)
		return
	}

	l := journal.NewListener(name, dir, port)
	if err := l.Start(cmd.Context()); err != nil {
		prov.Logger().Sugar().Warnf("journal listener: %v", err)
		return
	}
	if port != 19532 {
		fmt.Printf("Journal listener on port %d (standard port busy)\n", port)
	}
}

type (
	startTickMsg  struct{}
	startPhaseMsg struct {
		phase  boot.Phase
		detail string
		at     time.Time
	}
	startDoneMsg struct{ err error }
)

type startSpinnerModel struct {
	spinner    spinner.Model
	name       string
	status     string
	phase      boot.Phase
	phaseStart time.Time // when the VM first started, used pre-phase
	detail     string
	done       bool
	err        error
}

func (m *startSpinnerModel) Init() tea.Cmd { return m.spinner.Tick }

func (m *startSpinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case startDoneMsg:
		m.done = true
		m.err = msg.err
		return m, tea.Quit
	case startPhaseMsg:
		m.phase = msg.phase
		m.detail = msg.detail
		if !msg.at.IsZero() {
			m.phaseStart = msg.at
		}
		m.status = m.composeStatus()
	case startTickMsg:
		m.status = m.composeStatus()
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

func (m *startSpinnerModel) composeStatus() string {
	elapsed := time.Since(m.phaseStart).Truncate(time.Second)
	label := string(m.phase)
	if label == "" {
		label = "starting"
	}
	if m.detail != "" {
		return fmt.Sprintf("%s (%s)… %s", label, m.detail, elapsed)
	}
	return fmt.Sprintf("%s… %s", label, elapsed)
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

// streamSerialForeground tails the serial console log to stderr (ANSI-stripped)
// until ctx is cancelled. Intended for the --foreground install pass so the
// user can see live install progress without a separate `vee tunnel serial`.
func streamSerialForeground(ctx context.Context, logPath string) {
	dst := &ansiStripper{w: os.Stderr}

	var f *os.File
	// Poll until the file appears — QEMU may not have written it yet.
	for f == nil {
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
		fh, err := os.Open(logPath) //nolint:gosec // logPath is derived from vee-managed storage path and VM name.
		if err == nil {
			f = fh
		}
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 32*1024)
	for {
		select {
		case <-ctx.Done():
			// Drain any remaining bytes before returning.
			_, _ = io.CopyBuffer(dst, f, buf)
			return
		case <-time.After(250 * time.Millisecond):
			_, _ = io.CopyBuffer(dst, f, buf)
		}
	}
}
