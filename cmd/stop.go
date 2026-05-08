package cmd

import (
	"github.com/Benehiko/vee/vm"
	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:               "stop <name>",
	Short:             "Stop a running VM",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := vm.NewManager(prov)
		return mgr.Stop(cmd.Context(), args[0])
	},
}
