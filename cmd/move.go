package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/vm"
)

var (
	moveYes     bool
	moveNoStart bool
)

var moveCmd = &cobra.Command{
	Use:   "move <name> <target-dir>",
	Short: "Move a VM's boot disk to another directory",
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		// First arg: VM name. Second arg: target directory (default dir completion).
		if len(args) == 0 {
			return completeVMNames(cmd, args, toComplete)
		}
		if len(args) == 1 {
			return nil, cobra.ShellCompDirectiveFilterDirs
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	},
	Long: `Relocates a VM's managed boot qcow2 disk image into target-dir and updates the
VM config to point at the new location. Only the boot disk image moves — vm.yaml,
logs, control sockets, UEFI vars and cidata.iso stay under <storage_path>/<name>.

The VM must be shut down while its disk is moved. If it is running, vee stops it
first (prompting for confirmation), performs the move, and starts it again. Use
--yes to skip all prompts (for scripting) and --no-start to leave the VM stopped
after the move.

Examples:
  vee move linux-gaming /mnt/nvme/vms      — interactive: prompt to stop/restart
  vee move linux-gaming /mnt/nvme/vms -y   — non-interactive: stop, move, restart
  vee move linux-gaming /mnt/nvme/vms --no-start   — move, leave stopped`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		targetDir := args[1]

		mgr := vm.NewManager(prov)

		// Determine whether the VM is currently running.
		state, err := mgr.LoadState(name)
		if err != nil {
			return err
		}
		wasRunning := state.Running

		if wasRunning {
			if !moveYes {
				fmt.Printf("VM %q is running and must be stopped to move its boot disk.\n", name)
				if !confirm("Stop it now?", true) {
					return fmt.Errorf("aborted")
				}
			}
			fmt.Printf("Stopping %s…\n", name)
			if err := mgr.Stop(cmd.Context(), name); err != nil {
				return fmt.Errorf("stop VM: %w", err)
			}
		}

		oldPath, newPath, err := mgr.MoveBootDisk(name, targetDir)
		if err != nil {
			return err
		}
		fmt.Printf("Moved boot disk:\n  %s\n  → %s\n", oldPath, newPath)

		// Restart only if the VM was running before, and the caller didn't opt out.
		if !wasRunning || moveNoStart {
			return nil
		}

		restart := moveYes
		if !restart {
			restart = confirm(fmt.Sprintf("Start %s again?", name), true)
		}
		if !restart {
			return nil
		}

		fmt.Printf("Starting %s…\n", name)
		if err := mgr.Start(cmd.Context(), name, false); err != nil {
			return fmt.Errorf("start VM: %w", err)
		}
		return nil
	},
}

// confirm prompts the user for a yes/no answer on stdin. def is the default
// applied on an empty response.
func confirm(prompt string, def bool) bool {
	suffix := "[y/N]"
	if def {
		suffix = "[Y/n]"
	}
	fmt.Printf("%s %s: ", prompt, suffix)
	var answer string
	_, _ = fmt.Scanln(&answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer == "" {
		return def
	}
	return answer == "y" || answer == "yes"
}

func init() {
	moveCmd.Flags().BoolVarP(&moveYes, "yes", "y", false,
		"Skip all confirmation prompts (stop and restart automatically); use for scripting")
	moveCmd.Flags().BoolVar(&moveNoStart, "no-start", false,
		"Do not start the VM again after the move, even if it was running")
}
