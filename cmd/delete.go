package cmd

import (
	"github.com/Benehiko/vee/vm"
	"github.com/spf13/cobra"
)

var deleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a VM and its disks",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := vm.NewManager(prov)
		return mgr.Delete(args[0])
	},
}
