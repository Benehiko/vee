package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/vm"
)

var (
	resizeYes     bool
	resizeNoStart bool
)

var resizeCmd = &cobra.Command{
	Use:   "resize <name> <size>",
	Short: "Grow a VM's boot disk (e.g. 60G or +40G)",
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) == 0 {
			return completeVMNames(cmd, args, toComplete)
		}
		return nil, cobra.ShellCompDirectiveNoFileComp
	},
	Long: `Grows a VM's boot disk image to the given size and updates the VM config.
The size is either absolute ("60G") or a relative increase ("+40G").
Shrinking is refused — it would truncate the guest filesystem.

The VM must be shut down while its disk is resized. If it is running, vee
stops it first (prompting for confirmation), resizes the disk, and starts it
again. Use --yes to skip all prompts (for scripting) and --no-start to leave
the VM stopped after the resize.

Growing the image only adds unpartitioned space; the guest claims it itself:

  - Linux guests built by vee (cloud-init): automatic on the next boot —
    cloud-init's growpart expands the root filesystem.
  - Windows guests: extend C: in Disk Management, or
    powershell "Resize-Partition -DriveLetter C -Size (Get-PartitionSupportedSize -DriveLetter C).SizeMax"
  - macOS guests: diskutil apfs resizeContainer disk0s2 0

Examples:
  vee resize win11 60G       — grow the boot disk to 60G
  vee resize devbox +20G     — grow the boot disk by 20G
  vee resize win11 60G -y    — non-interactive: stop, resize, restart`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		size := args[1]

		mgr := vm.NewManager(prov)

		// Determine whether the VM is currently running.
		state, err := mgr.LoadState(name)
		if err != nil {
			return err
		}
		wasRunning := state.Running

		if wasRunning {
			if !resizeYes {
				fmt.Printf("VM %q is running and must be stopped to resize its boot disk.\n", name)
				if !confirm("Stop it now?", true) {
					return fmt.Errorf("aborted")
				}
			}
			fmt.Printf("Stopping %s…\n", name)
			if err := mgr.Stop(cmd.Context(), name); err != nil {
				return fmt.Errorf("stop VM: %w", err)
			}
		}

		res, err := mgr.ResizeBootDisk(cmd.Context(), name, size)
		if err != nil {
			return err
		}
		fmt.Printf("Resized boot disk: %s → %s\n  %s\n\n%s\n", res.OldSize, res.NewSize, res.Path, res.GuestHint)

		// Restart only if the VM was running before, and the caller didn't opt out.
		if !wasRunning || resizeNoStart {
			return nil
		}

		restart := resizeYes
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

func init() {
	resizeCmd.Flags().BoolVarP(&resizeYes, "yes", "y", false,
		"Skip all confirmation prompts (stop and restart automatically); use for scripting")
	resizeCmd.Flags().BoolVar(&resizeNoStart, "no-start", false,
		"Do not start the VM again after the resize, even if it was running")
}
