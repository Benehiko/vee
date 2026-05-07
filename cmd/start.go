package cmd

import (
	"github.com/Benehiko/vee/vm"
	"github.com/spf13/cobra"
)

var startForeground bool

var startCmd = &cobra.Command{
	Use:   "start <name>",
	Short: "Start a VM",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := vm.NewManager(prov)
		return mgr.Start(cmd.Context(), args[0], startForeground)
	},
}

func init() {
	startCmd.Flags().BoolVar(&startForeground, "foreground", false, "Run in foreground (block until VM exits)")
}
