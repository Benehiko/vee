package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/tui"
	"github.com/Benehiko/vee/internal/vm"
)

var configSSHPort int

var configCmd = &cobra.Command{
	Use:               "config [name]",
	Short:             "Edit an existing VM's configuration (TUI, or flags for scripted changes)",
	ValidArgsFunction: completeVMNames,
	Long: `Open a TUI form to edit a VM's configuration and save it to vm.yaml.

If a VM name is supplied the editor opens immediately.
If omitted, the VM list opens and you can navigate to the VM you want to edit.

With --ssh-port the change is made directly (no TUI): the port is saved to the
VM's config and, if the VM is running on user-mode NAT, forwarded live via QMP
so no restart is needed.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		if cmd.Flags().Changed("ssh-port") {
			if name == "" {
				return fmt.Errorf("--ssh-port requires a VM name")
			}
			mgr := vm.NewManager(prov)
			applied, err := mgr.SetSSHPort(cmd.Context(), name, configSSHPort)
			if err != nil {
				return err
			}
			if applied {
				fmt.Printf("SSH port set to %d and applied to the running VM — ssh -p %d 127.0.0.1\n", configSSHPort, configSSHPort)
			} else {
				fmt.Printf("SSH port set to %d; takes effect the next time %s starts\n", configSSHPort, name)
			}
			return nil
		}
		return tui.RunConfigEditor(cmd.Context(), prov, name)
	},
}

func init() {
	configCmd.Flags().IntVar(&configSSHPort, "ssh-port", 0, "Set the host port forwarded to VM port 22 (applied live to a running user-mode NAT VM)")
}
