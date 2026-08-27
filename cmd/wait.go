package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/vm"
)

var (
	waitTimeout   time.Duration
	waitCloudInit bool
)

var waitCmd = &cobra.Command{
	Use:   "wait <name>",
	Short: "Block until a running VM is usable over SSH",
	Long: `Blocks until an authenticated SSH command round-trip to the guest succeeds,
then exits 0. This is a stronger signal than the boot-time spinner, whose
readiness probe only checks that something accepts on the SSH port — which can
happen before the guest's authorized keys are in place.

With --cloud-init it additionally waits for the guest's first-boot
provisioning to finish (cloud-init status --wait; POSIX guests only).

Exits non-zero when the timeout passes or the VM's process exits.

Examples:
  vee wait myvm
  vee wait winvm --timeout 15m
  vee wait myvm --cloud-init && vee ssh myvm -- docker version`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := vm.NewManager(prov).WaitSSHReady(cmd.Context(), name, waitTimeout, waitCloudInit); err != nil {
			return err
		}
		fmt.Printf("VM %q is ready\n", name)
		return nil
	},
}

func init() {
	waitCmd.Flags().DurationVar(&waitTimeout, "timeout", 10*time.Minute, "Give up after this long")
	waitCmd.Flags().BoolVar(&waitCloudInit, "cloud-init", false, "Also wait for cloud-init to finish (POSIX guests)")
	rootCmd.AddCommand(waitCmd)
}
