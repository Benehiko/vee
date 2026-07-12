package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/runnercreds"
	"github.com/Benehiko/vee/internal/runnerssh"
	"github.com/Benehiko/vee/internal/vm"
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

var runnerKeyCmd = &cobra.Command{
	Use:   "key [name]",
	Short: "Print a runner's GitHub SSH public key (to add to GitHub)",
	Long: `Prints the SSH public key a runner uses to reach GitHub, for adding to the
GitHub dashboard.

With no argument, prints the shared GLOBAL key injected into every fresh runner,
generating it on first use. Add it once as an account SSH key, or as a per-repo
read-only Deploy key.

With a runner name, prints that runner's PER-INSTANCE key (created with
'vee create <name> --template github-runner --runner-ssh-key'). A per-instance
key lets you scope one runner to a single repo via a read-only Deploy key.

The public key is written to stdout (pipeable); guidance goes to stderr.

Examples:
  vee runner key
  vee runner key ci-1`,
	Args:              cobra.MaximumNArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(_ *cobra.Command, args []string) error {
		// Global key: generate on demand so the user can bootstrap and add it to
		// GitHub before ever creating a runner.
		if len(args) == 0 {
			id, err := runnercreds.LoadOrCreateIdentity()
			if err != nil {
				return fmt.Errorf("load age identity: %w", err)
			}
			pub, _, err := runnerssh.EnsureKey(id, "")
			if err != nil {
				return fmt.Errorf("ensure global runner ssh key: %w", err)
			}
			fmt.Fprintln(os.Stderr, "Global runner SSH public key — add to GitHub (account SSH key, or a per-repo read-only Deploy key):")
			fmt.Println(pub)
			return nil
		}

		// Per-instance key: report only, never generate (it is created at
		// 'vee create --runner-ssh-key' time and tied to that runner).
		name := args[0]
		pub, ok, err := runnerssh.PublicKey(name)
		if err != nil {
			return fmt.Errorf("read runner ssh key for %q: %w", name, err)
		}
		if !ok {
			return fmt.Errorf("no per-instance SSH key for %q — create one with: vee create %s --template github-runner --runner-ssh-key (or use 'vee runner key' for the shared global key)", name, name)
		}
		fmt.Fprintf(os.Stderr, "Per-instance runner SSH public key for %q — add to GitHub (per-repo read-only Deploy key recommended):\n", name)
		fmt.Println(pub)
		return nil
	},
}

func init() {
	runnerCmd.AddCommand(runnerSnapshotCmd)
	runnerCmd.AddCommand(runnerKeyCmd)
}
