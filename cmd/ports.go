package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/vm"
)

var portsCmd = &cobra.Command{
	Use:               "ports <name>",
	Short:             "List bound TCP ports and process names inside a running VM",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		_, state, err := loadRunningVM(name)
		if err != nil {
			return err
		}
		if state.QGASocket == "" {
			return fmt.Errorf("VM %q was not started with guest agent support; recreate with a template that enables guest_agent", name)
		}

		ports, err := vm.QueryGuestPorts(cmd.Context(), state.QGASocket, 5*time.Second)
		if err != nil {
			return err
		}
		if len(ports) == 0 {
			fmt.Println("no listening TCP ports found")
			return nil
		}

		fmt.Printf("%-8s %-20s %s\n", "PORT", "PROCESS", "ADDRESS")
		fmt.Println(strings.Repeat("-", 50))
		for _, p := range ports {
			fmt.Printf("%-8s %-20s %s\n", p.Port, p.Process, p.Addr)
		}
		return nil
	},
}
