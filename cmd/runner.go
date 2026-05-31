package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Benehiko/vee/internal/runnercreds"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/spf13/cobra"
)

var runnerCmd = &cobra.Command{
	Use:   "runner",
	Short: "Manage self-hosted GitHub Actions runner VMs",
	Long: `Commands for the github-runner template.

Runner registration credentials can be persisted to the host, encrypted with
age (~/.vee/age/identity.txt). A persisted snapshot lets 'vee create --reinstall
<name>' rejoin GitHub as the same runner without fetching a new registration
token or leaving a stale runner entry behind.`,
}

var runnerSnapshotCmd = &cobra.Command{
	Use:               "snapshot <name>",
	Short:             "Persist a runner's credentials to the host (encrypted)",
	Long:              "Pulls .credentials/.runner from a running runner VM and writes an age-encrypted snapshot to ~/.vee/runner-creds/<name>.age. Run this if the automatic snapshot during 'vee create' did not complete.",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		mgr := vm.NewManager(prov)

		cfg, err := mgr.LoadConfig(name)
		if err != nil {
			return fmt.Errorf("load config for %q: %w", name, err)
		}
		if cfg.Template != "github-runner" {
			return fmt.Errorf("%q is not a github-runner VM (template: %s)", name, cfg.Template)
		}
		state, err := mgr.LoadState(name)
		if err != nil {
			return fmt.Errorf("load state for %q: %w", name, err)
		}
		if !state.Running || state.SSHPort == 0 {
			return fmt.Errorf("runner %q must be running with an SSH port to snapshot", name)
		}

		user := cfg.SSHUser
		if user == "" && cfg.CloudInit != nil {
			user = cfg.CloudInit.User
		}

		id, err := runnercreds.LoadOrCreateIdentity()
		if err != nil {
			return fmt.Errorf("load age identity: %w", err)
		}

		ssh := runnercreds.NewSSHRunner(user, "127.0.0.1", state.SSHPort, veeIdentityPath())

		ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
		defer cancel()

		if err := runnercreds.Snapshot(ctx, ssh, id, name); err != nil {
			return fmt.Errorf("snapshot runner creds: %w", err)
		}
		path, _ := runnercreds.SnapshotPath(name)
		fmt.Fprintf(os.Stderr, "Persisted encrypted runner credentials to %s\n", path)
		return nil
	},
}

func init() {
	runnerCmd.AddCommand(runnerSnapshotCmd)
}
