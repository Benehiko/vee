package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/monitor"
	"github.com/Benehiko/vee/internal/vm"
)

var monitorCmd = &cobra.Command{
	Use:               "monitor <name>",
	Short:             "Real-time resource monitor for a running VM",
	Long:              "Displays memory, disk I/O, and network I/O stats polled via QMP. Press q to quit.",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		mgr := vm.NewManager(prov)
		state, err := mgr.LoadState(name)
		if err != nil {
			return fmt.Errorf("load state for %q: %w", name, err)
		}
		if !state.Running || state.QMPSocket == "" {
			return fmt.Errorf("VM %q is not running or has no QMP socket", name)
		}

		return monitor.Run(cmd.Context(), name, state.QMPSocket)
	},
}

func init() {
	rootCmd.AddCommand(monitorCmd)
}
