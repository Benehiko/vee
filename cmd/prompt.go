package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Benehiko/vee/internal/templates"
	"github.com/Benehiko/vee/internal/tui"
	"github.com/Benehiko/vee/internal/vpn"
)

var ghostStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

// textInputModel is a generic single-line text input bubbletea model.
// suggestions is called with the current value and returns tab-completion candidates.
type textInputModel struct {
	prompt      string
	input       textinput.Model
	suggestions func(string) []string
	cancelled   bool
}

func (m *textInputModel) Init() tea.Cmd { return textinput.Blink }

func (m *textInputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.Type {
		case tea.KeyEnter:
			return m, tea.Quit
		case tea.KeyCtrlC, tea.KeyEsc:
			m.cancelled = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.suggestions != nil {
		m.input.SetSuggestions(m.suggestions(m.input.Value()))
	}
	return m, cmd
}

func (m *textInputModel) View() string {
	return m.prompt + "\n" + m.input.View() + "\n" + ghostStyle.Render("Tab: complete  Enter: confirm  Esc: skip") + "\n"
}

func runTextInput(prompt, placeholder string, suggestions func(string) []string) (string, bool, error) {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Focus()
	if suggestions != nil {
		ti.ShowSuggestions = true
	}
	m := &textInputModel{prompt: prompt, input: ti, suggestions: suggestions}
	p := tea.NewProgram(m)
	result, err := p.Run()
	if err != nil {
		return "", false, err
	}
	final := result.(*textInputModel)
	return strings.TrimSpace(final.input.Value()), final.cancelled, nil
}

// promptPath displays prompt and reads a filesystem path from stdin with
// tab-completion and inline ghost text preview. Returns empty string if blank.
func promptPath(prompt string) (string, error) {
	val, cancelled, err := runTextInput(prompt, "leave blank to skip", tui.PathSuggestions)
	if err != nil || cancelled {
		return "", err
	}
	return val, nil
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

	fmt.Fprintln(os.Stderr, "Fetching countries...")
	countries, err := vpn.Countries()
	if err != nil {
		// Non-fatal: fall back to plain text prompt.
		fmt.Fprint(os.Stderr, "Country to connect to (leave blank for auto): ")
		country, _ := stdin.ReadString('\n')
		//nolint:nilerr // country fetch failure is intentionally non-fatal; fall back to manual entry.
		return &vpn.NordVPNConfig{Token: token, Country: strings.TrimSpace(country)}, nil
	}

	country, err := promptCountry("Country to connect to (leave blank for auto):", countries)
	if err != nil {
		return nil, err
	}

	return &vpn.NordVPNConfig{Token: token, Country: country}, nil
}

// promptCountry shows a text input with ghost-text completion from the given
// country list. Returns empty string if the user leaves it blank.
func promptCountry(prompt string, countries []string) (string, error) {
	val, _, err := runTextInput(prompt, "leave blank for auto", func(v string) []string {
		return countrySuggestions(v, countries)
	})
	return val, err
}

// countrySuggestions returns country names that have the given prefix (case-insensitive).
func countrySuggestions(prefix string, countries []string) []string {
	if prefix == "" {
		return nil
	}
	lower := strings.ToLower(prefix)
	var out []string
	for _, c := range countries {
		if strings.HasPrefix(strings.ToLower(c), lower) {
			out = append(out, c)
		}
	}
	return out
}

func promptGenericWireGuard() (*vpn.WireGuardConfig, error) {
	confPath, err := promptPath("Path to WireGuard .conf file: ")
	if err != nil || confPath == "" {
		return nil, err
	}
	data, err := os.ReadFile(confPath) //nolint:gosec // confPath is a user-supplied path the user intends to read.
	if err != nil {
		return nil, fmt.Errorf("read WireGuard config: %w", err)
	}
	wgConf, err := vpn.ParseWireGuardConf(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse WireGuard config: %w", err)
	}
	return wgConf, nil
}
