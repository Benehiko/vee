package cmd

import (
	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/vm"
)

var deleteCmd = &cobra.Command{
	Use:               "delete <name>",
	Short:             "Delete a VM and its disks",
	Long:              "Deletes the VM configuration, disks, and runtime state. The backups/ directory is preserved at ~/.vee/vms/<name>/backups/.",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := vm.NewManager(prov)
		return mgr.Delete(args[0])
	},
}
