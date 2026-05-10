package cmd

import (
	"github.com/Benehiko/vee/internal/vm"
	"github.com/spf13/cobra"
)

var stopForce bool

var stopCmd = &cobra.Command{
	Use:               "stop <name>",
	Short:             "Stop a running VM",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := vm.NewManager(prov)
		if stopForce {
			return mgr.ForceStop(cmd.Context(), args[0])
		}
		return mgr.Stop(cmd.Context(), args[0])
	},
}

func init() {
	stopCmd.Flags().BoolVar(&stopForce, "force", false,
		"Skip QMP graceful shutdown and SIGKILL the VM (use when a graceful stop has wedged)")
}
