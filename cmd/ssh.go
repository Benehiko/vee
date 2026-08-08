package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/platform"
	"github.com/Benehiko/vee/internal/vm"
)

var (
	sshUser       string
	sshIdentity   string
	sshExtraFlags []string
)

var sshCmd = &cobra.Command{
	Use:               "ssh [ssh-flags] <name> [-- <command>...]",
	Short:             "Open an SSH session into a running VM",
	ValidArgsFunction: completeSSHArgs,
	Long: `Connects to a running VM via SSH.

For headless VMs with a port-forward (--ssh-port), connects to 127.0.0.1 on
that port. For bridge-mode and vz (macOS guest) VMs, resolves the guest IP
by MAC address from the host's DHCP lease and ARP/neighbour tables.

The username defaults to the cloud-init user configured at creation time.
Override with --user (or ssh's -l).

ssh(1) flags are accepted directly, before the VM name — -L, -R, -D, -J, -A,
-o and the rest of the ssh flag surface are passed through untouched. Anything
after -- is the remote command; it is shell-quoted so the argv the guest sees
is exactly the argv given here.

Note that vee's own global flags (--verbose, --config, --mirror) must come
before the ssh subcommand, since -v after it belongs to ssh.

Examples:
  vee ssh myvm
  vee ssh myvm --user root
  vee ssh myvm --identity ~/.ssh/id_ed25519
  vee ssh -L 8080:localhost:8080 myvm
  vee ssh myvm -- sh -c 'echo "x  y"; echo done'
  vee -v ssh myvm`,
	// ssh's flag surface overlaps vee's (-v most notably, which cobra would
	// refuse to redefine), and clustered forms like -vvv are not something
	// pflag models. Parse the argv here instead so `vee ssh` accepts exactly
	// what ssh accepts.
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
			return cmd.Help()
		}

		parsed, err := parseSSHArgv(args)
		if err != nil {
			return err
		}
		name := parsed.Name
		sshUser = parsed.User
		sshIdentity = parsed.Identity
		sshExtraFlags = parsed.SSHFlags
		remoteCmd := parsed.Command

		cfg, state, err := loadRunningVM(name)
		if err != nil {
			return err
		}

		user := sshUser
		if user == "" {
			user = cfg.SSHUser
		}
		if user == "" && cfg.CloudInit != nil && cfg.CloudInit.User != "" {
			user = cfg.CloudInit.User
		}

		// For TrueNAS, default to stored admin user.
		if cfg.Template == "truenas" && user == "" {
			user = cfg.TrueNASUser
		}

		// Always prefer the vee SSH keypair when no identity is specified.
		if sshIdentity == "" {
			home, herr := os.UserHomeDir()
			if herr == nil {
				veeKey := home + "/.vee/ssh/id_ed25519"
				if _, statErr := os.Stat(veeKey); statErr == nil {
					sshIdentity = veeKey
				}
			}
		}

		var host string
		var port int

		// Dispatch on state, which records the backend that actually started
		// the VM. vz NAT has no host port-forwarding, so a recorded SSHPort
		// (states written before vz configs rejected ssh_port) must never
		// win — nothing listens on 127.0.0.1.
		vzBackend := state.BackendName() == backend.VZ

		// A recorded SSHPort is only meaningful for user-mode NAT — on a
		// bridge the hostfwd forwards nothing. The manager no longer records
		// one for bridge VMs, but states written by older versions still
		// carry the stale port, so gate on the effective NIC mode too. A
		// "bridge" config on a host without bridge support falls back to
		// user-mode NAT at start, where the port-forward is real.
		bridged := cfg.NIC.Mode == "bridge" && platform.SupportsBridgeNetworking()

		switch {
		case !vzBackend && !bridged && state.SSHPort > 0:
			// Headless user-mode port-forward.
			host = "127.0.0.1"
			port = state.SSHPort

		case vzBackend || cfg.NIC.Mode == "bridge" || cfg.NIC.Mode == "":
			// Bridge mode and vz NAT guests — resolve the IP from the host's
			// lease/neighbour tables by MAC first, fall back to QGA.
			mac := cfg.NIC.MAC
			if mac == "" {
				return fmt.Errorf("VM %q has no MAC address recorded; cannot resolve IP", name)
			}
			ip, resolveErr := vm.ResolveIPFromMAC(mac)
			if resolveErr != nil && state.QGASocket != "" {
				ip, resolveErr = vm.ResolveIPFromQGA(cmd.Context(), state.QGASocket)
			}
			if resolveErr != nil {
				return fmt.Errorf("could not resolve IP for VM %q (MAC %s): %w\nGet the IP with: vee ip %s", name, mac, resolveErr, name)
			}
			host = ip
			port = 22

		default:
			return fmt.Errorf("VM %q has no SSH port and is not on a bridge network; check --ssh-port or --nic-mode", name)
		}

		// Use a vee-managed known_hosts so that recreated VMs don't pollute
		// ~/.ssh/known_hosts. Scrub any stale entry for this host before
		// connecting — vee tracks VM identity, so a changed host key always
		// means reinstall, never MITM.
		veeKnownHosts := ""
		if home, herr := os.UserHomeDir(); herr == nil {
			veeKnownHosts = home + "/.vee/ssh/known_hosts"
		}
		if veeKnownHosts != "" {
			scrubKnownHost(veeKnownHosts, host, port)
		}

		sshArgs := buildSSHArgs(user, host, port, sshIdentity, veeKnownHosts, remoteCmd, sshExtraFlags)

		sshBin, err := exec.LookPath("ssh")
		if err != nil {
			return fmt.Errorf("ssh not found in PATH: %w", err)
		}

		// Hand off to ssh. On unix this replaces the current process (execve) so
		// signals flow naturally; on Windows it spawns ssh as a child and waits.
		// See ssh_exec_unix.go / ssh_exec_windows.go.
		return execSSH(sshBin, sshArgs, sshEnv(os.Environ(), vzBackend))
	},
}

// macOSTerminfoPrefixes are the terminal types a stock macOS install has
// terminfo entries for. Anything else (xterm-ghostty, xterm-kitty, wezterm,
// alacritty, …) is unknown to a macOS guest.
var macOSTerminfoPrefixes = []string{"xterm", "screen", "tmux", "vt", "ansi", "dumb", "linux", "rxvt", "putty", "nsterm", "Apple"}

// sshEnv adapts the environment ssh inherits. ssh sends its own TERM when it
// requests a pty, and a macOS guest only has the terminfo database Apple
// ships: connecting from a modern terminal (Ghostty sends TERM=xterm-ghostty)
// leaves the guest without an entry, and zsh's line editor then garbles input.
// Fall back to a type the guest is guaranteed to know.
func sshEnv(env []string, vzGuest bool) []string {
	if !vzGuest {
		return env
	}
	term := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "TERM="); ok {
			term = v
		}
	}
	if term == "" || knownToMacOSTerminfo(term) {
		return env
	}
	fmt.Fprintf(os.Stderr, "note: the guest has no terminfo entry for TERM=%s; using xterm-256color for this session\n", term)
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "TERM=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "TERM=xterm-256color")
}

// knownToMacOSTerminfo reports whether a macOS guest is likely to have a
// terminfo entry for term. Apple's database covers the classic families; the
// terminal-specific entries shipped by newer emulators are not present.
func knownToMacOSTerminfo(term string) bool {
	for _, prefix := range macOSTerminfoPrefixes {
		if strings.HasPrefix(term, prefix) {
			// xterm-ghostty and xterm-kitty share the xterm prefix but are
			// their own entries, which macOS does not ship.
			if prefix == "xterm" && strings.Count(term, "-") > 0 {
				switch term {
				case "xterm", "xterm-color", "xterm-16color", "xterm-88color", "xterm-256color", "xterm-new", "xterm-direct":
					return true
				default:
					return false
				}
			}
			return true
		}
	}
	return false
}

func buildSSHArgs(user, host string, port int, identity, knownHosts string, remoteCmd, extra []string) []string {
	var args []string
	if port != 22 {
		args = append(args, "-p", fmt.Sprintf("%d", port))
	}
	if identity != "" {
		args = append(args, "-i", identity)
	}
	// extra holds the user's ssh flags (-L 8080:..., -o ...) — before host, and
	// ahead of vee's own -o defaults below: ssh honours the first value it sees
	// for any given option, so a user -o has to come first to take effect. A
	// user -p is the exception (ssh takes the last one), which is also what we
	// want — an explicit port overrides the one vee resolved.
	args = append(args, extra...)
	if knownHosts != "" {
		args = append(args, "-o", "UserKnownHostsFile="+knownHosts)
		args = append(args, "-o", "StrictHostKeyChecking=accept-new")
	}

	dest := host
	if user != "" {
		dest = user + "@" + host
	}
	args = append(args, dest)

	// remoteCmd holds the remote command argv — after host. ssh joins its
	// remote-command arguments with single spaces and hands the result to the
	// remote shell, which re-splits and re-evaluates it, so every argument has
	// to be shell-quoted to survive the round trip and reach the guest as the
	// same argv the caller typed.
	if len(remoteCmd) > 0 {
		quoted := make([]string, len(remoteCmd))
		for i, a := range remoteCmd {
			quoted[i] = shellQuote(a)
		}
		args = append(args, strings.Join(quoted, " "))
	}
	return args
}

// shellQuote renders s as a single POSIX shell word. Single quotes disable
// every form of expansion, so the only character needing care is the single
// quote itself: close the string, emit an escaped quote, reopen.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	// Unquoted is safe only for characters no shell assigns meaning to.
	if strings.IndexFunc(s, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		}
		return !strings.ContainsRune("@%_-+=:,./", r)
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func init() {
	// Registered so --help lists them; sshCmd sets DisableFlagParsing, so
	// parseSSHArgv is what actually reads them.
	sshCmd.Flags().StringVarP(&sshUser, "user", "u", "", "SSH username (default: cloud-init user; ssh's -l is also accepted)")
	sshCmd.Flags().StringVarP(&sshIdentity, "identity", "i", "", "SSH identity file (private key; same as ssh's -i)")
	sshCmd.Flags().StringArrayVar(&sshExtraFlags, "ssh-flag", nil, "Extra flags passed to ssh(1) (repeatable)")

	// Shadow the root command's persistent flags with hidden local copies.
	// Cobra completes a command's own and inherited flags before consulting
	// ValidArgsFunction, and it skips hidden ones — without this, `vee ssh -`
	// would offer --verbose/--config/--mirror, which do not work in this
	// position: flag parsing is disabled here, so they reach ssh as
	// remote-command words. They still work before the subcommand
	// (`vee -v ssh myvm`), which is what the help text tells the user to do.
	// The values are never read; only the flags' presence matters.
	sshCmd.Flags().BoolP("verbose", "v", false, "")
	sshCmd.Flags().String("config", "", "")
	sshCmd.Flags().String("mirror", "", "")
	for _, name := range []string{"verbose", "config", "mirror"} {
		if err := sshCmd.Flags().MarkHidden(name); err != nil {
			panic(err) // only fails for a flag that was not just registered
		}
	}
}

// scrubKnownHost removes all lines matching host (and [host]:port for non-22
// ports) from the vee-managed known_hosts file. Called before every connect so
// a reinstalled VM with the same IP never blocks with a host-key-changed error.
func scrubKnownHost(knownHostsPath, host string, port int) {
	data, err := os.ReadFile(knownHostsPath) //nolint:gosec // knownHostsPath is the fixed vee-managed ~/.vee/ssh/known_hosts.
	if err != nil {
		return
	}

	// Build the key patterns we want to drop: bare host for port 22,
	// bracketed [host]:port for any other port.
	drop := host
	if port != 22 {
		drop = fmt.Sprintf("[%s]:%d", host, port)
	}

	var kept []string
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := scanner.Text()
		// Each known_hosts line starts with the host pattern (possibly
		// comma-separated), then a space, then the key type and key.
		fields := strings.Fields(line)
		if len(fields) == 0 {
			kept = append(kept, line)
			continue
		}
		if !slices.Contains(strings.Split(fields[0], ","), drop) {
			kept = append(kept, line)
		}
	}

	out := strings.Join(kept, "\n")
	if len(kept) > 0 {
		out += "\n"
	}
	//nolint:gosec // knownHostsPath is the fixed vee-managed ~/.vee/ssh/known_hosts.
	_ = os.WriteFile(knownHostsPath, []byte(out), 0o600)
}
