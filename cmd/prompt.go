package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Benehiko/vee/templates"
	"github.com/Benehiko/vee/vpn"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var ghostStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

// promptPath displays prompt and reads a filesystem path from stdin with
// tab-completion and inline ghost text preview. Returns empty string if blank.
func promptPath(prompt string) (string, error) {
	ti := textinput.New()
	ti.Placeholder = "leave blank to skip"
	ti.Focus()
	ti.ShowSuggestions = true

	m := &pathInputModel{
		prompt: prompt,
		input:  ti,
	}
	p := tea.NewProgram(m)
	result, err := p.Run()
	if err != nil {
		return "", err
	}
	final := result.(*pathInputModel)
	if final.cancelled {
		return "", nil
	}
	return strings.TrimSpace(final.input.Value()), nil
}

type pathInputModel struct {
	prompt    string
	input     textinput.Model
	cancelled bool
	done      bool
}

func (m *pathInputModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m *pathInputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			m.done = true
			return m, tea.Quit
		case tea.KeyCtrlC, tea.KeyEsc:
			m.cancelled = true
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.input.SetSuggestions(pathSuggestions(m.input.Value()))
	return m, cmd
}

func (m *pathInputModel) View() string {
	return m.prompt + "\n" + m.input.View() + "\n" + ghostStyle.Render("Tab: complete  Enter: confirm  Esc: skip") + "\n"
}

// promptShareMounts interactively collects host→guest directory pairs until the
// user submits a blank host path. If prefillHostDir is non-empty it is used as
// the first host path without prompting.
func promptShareMounts(prefillHostDir string) ([]templates.ShareMount, error) {
	var mounts []templates.ShareMount
	stdin := bufio.NewReader(os.Stdin)

	first := true
	for {
		var hostDir string
		if first && prefillHostDir != "" {
			hostDir = prefillHostDir
			first = false
		} else {
			var err error
			hostDir, err = promptPath("Host directory to mount (leave blank to finish): ")
			if err != nil {
				return nil, err
			}
			hostDir = strings.TrimSpace(hostDir)
			if hostDir == "" {
				break
			}
			first = false
		}

		fmt.Fprintf(os.Stderr, "Guest mount point for %s (e.g. /downloads): ", hostDir)
		guestPath, _ := stdin.ReadString('\n')
		guestPath = strings.TrimSpace(guestPath)
		if guestPath == "" {
			guestPath = "/downloads"
			if len(mounts) > 0 {
				guestPath = fmt.Sprintf("/share%d", len(mounts))
			}
		}
		mounts = append(mounts, templates.ShareMount{HostDir: hostDir, GuestPath: guestPath})
	}
	return mounts, nil
}

// promptVPN interactively asks whether to configure a VPN for the torrent VM.
// Returns (nordConf, wgConf, providerName, error). At most one of nordConf/wgConf is non-nil.
func promptVPN() (*vpn.NordVPNConfig, *vpn.WireGuardConfig, string, error) {
	stdin := bufio.NewReader(os.Stdin)

	fmt.Fprint(os.Stderr, "Configure VPN? [y/N]: ")
	answer, _ := stdin.ReadString('\n')
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y") {
		return nil, nil, "", nil
	}

	fmt.Fprintln(os.Stderr, "Provider:")
	fmt.Fprintln(os.Stderr, "  1) NordVPN (access token — from my.nordaccount.com/dashboard/nordvpn/access-tokens/)")
	fmt.Fprintln(os.Stderr, "  2) Generic WireGuard config file")
	fmt.Fprint(os.Stderr, "Choice [1]: ")
	choice, _ := stdin.ReadString('\n')
	choice = strings.TrimSpace(choice)

	switch choice {
	case "", "1":
		nord, err := promptNordVPN(stdin)
		return nord, nil, "nordvpn", err
	case "2":
		wg, err := promptGenericWireGuard()
		return nil, wg, "generic", err
	default:
		return nil, nil, "", fmt.Errorf("invalid choice %q", choice)
	}
}

func promptNordVPN(stdin *bufio.Reader) (*vpn.NordVPNConfig, error) {
	fmt.Fprintln(os.Stderr, "Generate a token at: my.nordaccount.com/dashboard/nordvpn/access-tokens/")
	fmt.Fprint(os.Stderr, "NordVPN access token: ")
	token, _ := stdin.ReadString('\n')
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("NordVPN access token is required")
	}

	fmt.Fprintln(os.Stderr, "Validating token...")
	if err := vpn.ValidateToken(token); err != nil {
		return nil, err
	}

	fmt.Fprint(os.Stderr, "Country to connect to (leave blank for auto): ")
	country, _ := stdin.ReadString('\n')
	country = strings.TrimSpace(country)

	return &vpn.NordVPNConfig{Token: token, Country: country}, nil
}

func promptGenericWireGuard() (*vpn.WireGuardConfig, error) {
	confPath, err := promptPath("Path to WireGuard .conf file: ")
	if err != nil || confPath == "" {
		return nil, err
	}
	data, err := os.ReadFile(confPath)
	if err != nil {
		return nil, fmt.Errorf("read WireGuard config: %w", err)
	}
	wgConf, err := vpn.ParseWireGuardConf(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse WireGuard config: %w", err)
	}
	return wgConf, nil
}

// pathSuggestions returns filesystem completions for the current input value.
func pathSuggestions(prefix string) []string {
	dir, partial := filepath.Split(prefix)
	if dir == "" {
		dir = "."
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var completions []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, partial) {
			continue
		}
		full := filepath.Join(dir, name)
		if dir != "." {
			full = filepath.Join(dir, name)
		}
		completions = append(completions, full+"/")
	}
	return completions
}
