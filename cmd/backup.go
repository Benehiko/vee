package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

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
			// New run: enumerate guest dirs and show picker.
			fmt.Println("Enumerating guest directories...")
			entries, err := backup.EnumerateHome(conn, 6)
			if err != nil {
				return fmt.Errorf("enumerate: %w", err)
			}
			if len(entries) == 0 {
				return fmt.Errorf("no directories found on guest")
			}

			dirs, err = tui.RunBackupPicker(entries)
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

func buildSSHConn(cfg *vm.VMConfig, state *vm.VMState) (backup.SSHConn, error) {
	user := cfg.SSHUser
	if user == "" && cfg.CloudInit != nil && cfg.CloudInit.User != "" {
		user = cfg.CloudInit.User
	}

	identity := ""
	if home, err := os.UserHomeDir(); err == nil {
		veeKey := filepath.Join(home, ".vee", "ssh", "id_ed25519")
		if _, err := os.Stat(veeKey); err == nil {
			identity = veeKey
		}
	}

	var host string
	var port int
	switch {
	case state.SSHPort > 0:
		host = "127.0.0.1"
		port = state.SSHPort
	case cfg.NIC.Mode == "bridge" || cfg.NIC.Mode == "":
		mac := cfg.NIC.MAC
		if mac == "" {
			return backup.SSHConn{}, fmt.Errorf("VM %q has no MAC address; cannot resolve IP", cfg.Name)
		}
		ip, err := vm.ResolveIPFromMAC(mac)
		if err != nil {
			return backup.SSHConn{}, fmt.Errorf("resolve IP for %q: %w", cfg.Name, err)
		}
		host = ip
		port = 22
	default:
		return backup.SSHConn{}, fmt.Errorf("VM %q has no SSH port and is not on a bridge network", cfg.Name)
	}

	return backup.SSHConn{User: user, Host: host, Port: port, Identity: identity}, nil
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
