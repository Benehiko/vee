package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Benehiko/vee/vpn"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
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

// promptVPN interactively asks the user whether to configure a VPN for the
// torrent VM, and if so, which provider. Returns the WireGuard config and
// provider name, or nil/"" to skip.
func promptVPN() (*vpn.WireGuardConfig, string, error) {
	stdin := bufio.NewReader(os.Stdin)

	fmt.Fprint(os.Stderr, "Configure VPN? [y/N]: ")
	answer, _ := stdin.ReadString('\n')
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "y") {
		return nil, "", nil
	}

	fmt.Fprintln(os.Stderr, "Provider:")
	fmt.Fprintln(os.Stderr, "  1) NordVPN")
	fmt.Fprintln(os.Stderr, "  2) Generic WireGuard config file")
	fmt.Fprint(os.Stderr, "Choice [1]: ")
	choice, _ := stdin.ReadString('\n')
	choice = strings.TrimSpace(choice)

	switch choice {
	case "", "1":
		return promptNordVPN(stdin)
	case "2":
		return promptGenericWireGuard()
	default:
		return nil, "", fmt.Errorf("invalid choice %q", choice)
	}
}

func promptNordVPN(stdin *bufio.Reader) (*vpn.WireGuardConfig, string, error) {
	fmt.Fprint(os.Stderr, "NordVPN username (email): ")
	username, _ := stdin.ReadString('\n')
	username = strings.TrimSpace(username)

	fmt.Fprint(os.Stderr, "NordVPN password: ")
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, "", fmt.Errorf("read password: %w", err)
	}

	fmt.Fprintln(os.Stderr, "Authenticating with NordVPN...")
	client, err := vpn.NewNordVPNClient(username, string(pw))
	if err != nil {
		return nil, "", fmt.Errorf("NordVPN auth: %w", err)
	}

	countries, err := vpn.Countries()
	if err != nil {
		return nil, "", fmt.Errorf("fetch countries: %w", err)
	}

	fmt.Fprint(os.Stderr, "Country (leave blank for recommended): ")
	countryInput, _ := stdin.ReadString('\n')
	countryInput = strings.TrimSpace(countryInput)

	var countryID int
	if countryInput != "" {
		lower := strings.ToLower(countryInput)
		for _, c := range countries {
			if strings.ToLower(c.Name) == lower || strings.ToLower(c.Code) == lower {
				countryID = c.ID
				break
			}
		}
		if countryID == 0 {
			return nil, "", fmt.Errorf("unknown country %q", countryInput)
		}
	}

	fmt.Fprintln(os.Stderr, "Finding best server...")
	server, err := vpn.RecommendedServer(countryID)
	if err != nil {
		return nil, "", fmt.Errorf("find server: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Selected: %s (%s, load %d%%)\n", server.Hostname, server.Country, server.Load)

	fmt.Fprintln(os.Stderr, "Generating WireGuard keys and registering with NordVPN...")
	wgConf, err := client.GenerateConfig(server)
	if err != nil {
		return nil, "", fmt.Errorf("generate WireGuard config: %w", err)
	}

	return wgConf, "nordvpn", nil
}

func promptGenericWireGuard() (*vpn.WireGuardConfig, string, error) {
	confPath, err := promptPath("Path to WireGuard .conf file: ")
	if err != nil || confPath == "" {
		return nil, "", err
	}
	data, err := os.ReadFile(confPath)
	if err != nil {
		return nil, "", fmt.Errorf("read WireGuard config: %w", err)
	}
	wgConf, err := vpn.ParseWireGuardConf(string(data))
	if err != nil {
		return nil, "", fmt.Errorf("parse WireGuard config: %w", err)
	}
	return wgConf, "generic", nil
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
