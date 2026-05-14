package cmd

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Benehiko/vee/internal/backup"
	"github.com/Benehiko/vee/internal/tui"
	"github.com/Benehiko/vee/internal/vm"
)

var backupList bool

var backupCmd = &cobra.Command{
	Use:               "backup <name>",
	Short:             "Back up directories from a running VM",
	ValidArgsFunction: completeVMNames,
	Long: `Backs up selected guest directories to ~/.vee/vms/<name>/backups/<date>/ via
rsync over SSH. A TUI dir-picker lets you choose which directories to include.

Each run is recorded in the vee database. If a previous run failed or was
interrupted, vee will offer to retry it using the same directory selection
without re-running the picker.

Examples:
  vee backup linux-gaming          — interactive dir picker, then run
  vee backup linux-gaming --list   — show past backup runs`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if backupList {
			return listBackupRuns(name)
		}

		cfg, state, err := loadRunningVM(name)
		if err != nil {
			return err
		}

		conn, err := buildSSHConn(cfg, state)
		if err != nil {
			return err
		}

		db := prov.DB()

		// Check for a previous incomplete run and offer retry.
		last, err := backup.LastIncomplete(db, name)
		if err != nil {
			return fmt.Errorf("check previous runs: %w", err)
		}

		var runID int64
		var dirs []string
		var dest string

		if last != nil {
			fmt.Printf("Previous %s run found (id=%d, dest=%s)\n", last.Status, last.ID, last.Dest)
			fmt.Print("Re-use same directory selection? [Y/n]: ")
			var answer string
			_, _ = fmt.Scanln(&answer)
			answer = strings.ToLower(strings.TrimSpace(answer))

			if answer == "" || answer == "y" || answer == "yes" {
				dirs = last.Dirs
				dest = dateDest(name, last.Dest)
				runID, err = backup.CreateRun(db, name, dest, dirs)
				if err != nil {
					return fmt.Errorf("create run: %w", err)
				}
			}
		}

		if runID == 0 {
			dirs, err = tui.RunBackupLoader(conn, db, name)
			if err != nil {
				return fmt.Errorf("picker: %w", err)
			}
			if len(dirs) == 0 {
				fmt.Println("No directories selected.")
				return nil
			}

			dest = defaultDest(name)
			runID, err = backup.CreateRun(db, name, dest, dirs)
			if err != nil {
				return fmt.Errorf("create run: %w", err)
			}
		}

		if err := os.MkdirAll(dest, 0o755); err != nil {
			return fmt.Errorf("create dest %s: %w", dest, err)
		}

		fmt.Printf("\nBacking up %s → %s\n", name, dest)
		for _, d := range dirs {
			fmt.Printf("  %s\n", d)
		}
		fmt.Println()

		runner := &backup.Runner{DB: db, Conn: conn}
		if err := runner.Execute(runID, dest, dirs); err != nil {
			fmt.Fprintf(os.Stderr, "backup failed: %v\n", err)
			return err
		}

		fmt.Printf("\nDone. Backup stored at %s\n", dest)
		return nil
	},
}

func listBackupRuns(name string) error {
	db := prov.DB()
	runs, err := backup.ListRuns(db, name)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Printf("No backup runs for %s.\n", name)
		return nil
	}
	fmt.Printf("%-6s  %-10s  %-52s  %s\n", "ID", "STATUS", "DEST", "STARTED")
	fmt.Println(strings.Repeat("─", 90))
	for _, r := range runs {
		started := "—"
		if r.StartedAt != nil {
			started = r.StartedAt.Format("2006-01-02 15:04")
		}
		dest := r.Dest
		if len(dest) > 50 {
			dest = "…" + dest[len(dest)-49:]
		}
		fmt.Printf("%-6d  %-10s  %-52s  %s\n", r.ID, r.Status, dest, started)
		if r.Error != "" {
			fmt.Printf("       error: %s\n", r.Error)
		}
	}
	return nil
}

func veeIdentityPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, ".vee", "ssh", "id_ed25519")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func buildSSHConn(cfg *vm.VMConfig, state *vm.VMState) (backup.SSHConn, error) {
	user := cfg.SSHUser
	if user == "" && cfg.CloudInit != nil && cfg.CloudInit.User != "" {
		user = cfg.CloudInit.User
	}

	identity := veeIdentityPath()

	var host string
	var port int

	switch {
	case cfg.SSHHost != "":
		// Persisted override — use directly.
		h, p, err := parseSSHHost(cfg.SSHHost)
		if err != nil {
			return backup.SSHConn{}, fmt.Errorf("parse ssh_host %q: %w", cfg.SSHHost, err)
		}
		host, port = h, p

	case state.SSHPort > 0:
		host = "127.0.0.1"
		port = state.SSHPort

	case cfg.NIC.Mode == "bridge" || cfg.NIC.Mode == "":
		mac := cfg.NIC.MAC
		if mac != "" {
			ip, err := vm.ResolveIPFromMAC(mac)
			if err == nil {
				host = ip
				port = 22
				break
			}
		}
		// MAC resolution failed — fall through to interactive prompt.
		fallthrough

	default:
		// No automatic resolution possible; ask the user.
		conn, err := promptSSHConn(cfg.Name)
		if err != nil {
			return backup.SSHConn{}, err
		}
		// Try to inject the vee public key so future runs are passwordless.
		if identity != "" {
			if injErr := injectSSHKey(conn, identity+".pub"); injErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not inject SSH key: %v\n", injErr)
			} else {
				fmt.Println("SSH key injected — future connections will be passwordless.")
				conn.Identity = identity
			}
		}
		// Persist host and user so we never prompt again.
		cfg.SSHHost = net.JoinHostPort(conn.Host, fmt.Sprintf("%d", conn.Port))
		cfg.SSHUser = conn.User
		if err := vm.NewManager(prov).SaveConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not persist SSH connection info: %v\n", err)
		}
		return conn, nil
	}

	return backup.SSHConn{User: user, Host: host, Port: port, Identity: identity}, nil
}

// parseSSHHost splits "host", "host:port", or "[host]:port" into (host, port).
// Port defaults to 22.
func parseSSHHost(s string) (string, int, error) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		// No port — treat whole string as host.
		return s, 22, nil
	}
	var port int
	if _, err := fmt.Sscan(p, &port); err != nil || port <= 0 {
		return "", 0, fmt.Errorf("invalid port %q", p)
	}
	return h, port, nil
}

// promptSSHConn asks the user for a connection string and password,
// returning a populated SSHConn (without identity — key injection happens later).
func promptSSHConn(vmName string) (backup.SSHConn, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("Cannot resolve IP for %q automatically.\n", vmName)
	fmt.Print("SSH connection (user@host or user@host:port): ")
	raw, err := reader.ReadString('\n')
	if err != nil {
		return backup.SSHConn{}, fmt.Errorf("read connection string: %w", err)
	}
	raw = strings.TrimSpace(raw)

	var user, hostport string
	if idx := strings.Index(raw, "@"); idx > 0 {
		user = raw[:idx]
		hostport = raw[idx+1:]
	} else {
		return backup.SSHConn{}, fmt.Errorf("expected user@host, got %q", raw)
	}

	host, port, err := parseSSHHost(hostport)
	if err != nil {
		return backup.SSHConn{}, err
	}

	fmt.Printf("Password for %s: ", raw)
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return backup.SSHConn{}, fmt.Errorf("read password: %w", err)
	}

	return backup.SSHConn{User: user, Host: host, Port: port, Password: string(pw)}, nil
}

// injectSSHKey copies pubKeyPath into the remote authorized_keys via ssh-copy-id.
func injectSSHKey(conn backup.SSHConn, pubKeyPath string) error {
	if _, err := os.Stat(pubKeyPath); err != nil {
		return fmt.Errorf("public key not found: %w", err)
	}
	target := fmt.Sprintf("%s@%s", conn.User, net.JoinHostPort(conn.Host, fmt.Sprintf("%d", conn.Port)))
	args := []string{
		"-i", pubKeyPath,
		"-o", "StrictHostKeyChecking=no",
		"-p", fmt.Sprintf("%d", conn.Port),
	}
	if conn.Password != "" {
		// ssh-copy-id can't take a password directly; use sshpass if available.
		if sshpass, err := exec.LookPath("sshpass"); err == nil {
			fullArgs := append([]string{"-p", conn.Password, "ssh-copy-id"}, args...)
			fullArgs = append(fullArgs, target)
			cmd := exec.Command(sshpass, fullArgs...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
		// sshpass not available — fall back to interactive (user re-enters password).
		fmt.Println("(sshpass not found; you may be prompted for your password again)")
	}
	args = append(args, target)
	cmd := exec.Command("ssh-copy-id", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func defaultDest(vmName string) string {
	home, _ := os.UserHomeDir()
	stamp := time.Now().Format("2006-01-02")
	return filepath.Join(home, ".vee", "vms", vmName, "backups", stamp)
}

// dateDest returns a fresh date-stamped dest, reusing the parent dir of a
// previous dest (so re-runs land in the same backups/ folder).
func dateDest(vmName, prevDest string) string {
	stamp := time.Now().Format("2006-01-02")
	parent := filepath.Dir(prevDest)
	// If the previous dest's parent looks like the backups dir, reuse it.
	if strings.HasSuffix(parent, "/backups") || strings.HasSuffix(parent, string(os.PathSeparator)+"backups") {
		return filepath.Join(parent, stamp)
	}
	return defaultDest(vmName)
}

func init() {
	backupCmd.Flags().BoolVar(&backupList, "list", false, "list past backup runs for this VM")
}
