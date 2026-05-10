package cmd

import (
	"fmt"

	"github.com/Benehiko/vee/internal/vm"
	"github.com/spf13/cobra"
)

var autostartCmd = &cobra.Command{
	Use:               "autostart <name> [on|off]",
	Short:             "Enable or disable autostart for a VM",
	Long:              "With no second argument, prints the current autostart status. Pass 'on' or 'off' to change it.",
	Args:              cobra.RangeArgs(1, 2),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		mgr := vm.NewManager(prov)

		if len(args) == 1 {
			cfg, err := mgr.LoadConfig(name)
			if err != nil {
				return err
			}
			if cfg.AutoStart {
				fmt.Printf("%s: autostart on\n", name)
			} else {
				fmt.Printf("%s: autostart off\n", name)
			}
			return nil
		}

		switch args[1] {
		case "on":
			if err := mgr.SetAutoStart(name, true); err != nil {
				return err
			}
			fmt.Printf("%s: autostart enabled\n", name)
		case "off":
			if err := mgr.SetAutoStart(name, false); err != nil {
				return err
			}
			fmt.Printf("%s: autostart disabled\n", name)
		default:
			return fmt.Errorf("unknown value %q — use 'on' or 'off'", args[1])
		}
		return nil
	},
}
