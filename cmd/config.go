package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/tui"
	"github.com/Benehiko/vee/internal/vm"
)

var (
	configSSHPort    int
	configPointer    string
	configCPUPinning string
	configMemory     string
	configCPUs       int
	configNICMode    string
	configNICBridge  string
)

var configCmd = &cobra.Command{
	Use:               "config [name]",
	Short:             "Edit an existing VM's configuration (TUI, or flags for scripted changes)",
	ValidArgsFunction: completeVMNames,
	Long: `Open a TUI form to edit a VM's configuration and save it to vm.yaml.

If a VM name is supplied the editor opens immediately.
If omitted, the VM list opens and you can navigate to the VM you want to edit.

With --ssh-port the change is made directly (no TUI): the port is saved to the
VM's config and, if the VM is running on user-mode NAT, forwarded live via QMP
so no restart is needed. --pointer, --cpu-pinning, --memory, --cpus, and
--nic-mode likewise skip the TUI and save directly, taking effect on the VM's
next start.`,
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
		if cmd.Flags().Changed("pointer") {
			if name == "" {
				return fmt.Errorf("--pointer requires a VM name")
			}
			mgr := vm.NewManager(prov)
			if err := mgr.SetPointer(name, configPointer); err != nil {
				return err
			}
			fmt.Printf("pointer set to %s; takes effect the next time %s starts\n", configPointer, name)
			return nil
		}
		if cmd.Flags().Changed("cpu-pinning") {
			if name == "" {
				return fmt.Errorf("--cpu-pinning requires a VM name")
			}
			cpus, err := vm.ParseCPUPinning(configCPUPinning)
			if err != nil {
				return err
			}
			mgr := vm.NewManager(prov)
			if err := mgr.SetCPUPinning(name, cpus); err != nil {
				return err
			}
			if len(cpus) == 0 {
				fmt.Printf("CPU pinning cleared; takes effect the next time %s starts\n", name)
			} else {
				fmt.Printf("CPU pinning set to %s; takes effect the next time %s starts\n", configCPUPinning, name)
			}
			return nil
		}
		if cmd.Flags().Changed("memory") {
			if name == "" {
				return fmt.Errorf("--memory requires a VM name")
			}
			mgr := vm.NewManager(prov)
			if err := mgr.SetMemory(name, configMemory); err != nil {
				return err
			}
			fmt.Printf("memory set to %s; takes effect the next time %s starts\n", configMemory, name)
			return nil
		}
		if cmd.Flags().Changed("cpus") {
			if name == "" {
				return fmt.Errorf("--cpus requires a VM name")
			}
			mgr := vm.NewManager(prov)
			if err := mgr.SetCPUs(name, configCPUs); err != nil {
				return err
			}
			fmt.Printf("cpus set to %d; takes effect the next time %s starts\n", configCPUs, name)
			return nil
		}
		if cmd.Flags().Changed("nic-mode") {
			if name == "" {
				return fmt.Errorf("--nic-mode requires a VM name")
			}
			mgr := vm.NewManager(prov)
			if err := mgr.SetNICMode(name, configNICMode, configNICBridge); err != nil {
				return err
			}
			fmt.Printf("nic mode set to %s; takes effect the next time %s starts\n", configNICMode, name)
			return nil
		}
		return tui.RunConfigEditor(cmd.Context(), prov, name)
	},
}

func init() {
	configCmd.Flags().StringVar(&configPointer, "pointer", "", "Set the guest pointing device for a virtio-GPU VM: tablet (absolute, desktop default) or mouse (relative, for pointer-locked games); applies on the next start")
	configCmd.Flags().IntVar(&configSSHPort, "ssh-port", 0, "Set the host port forwarded to VM port 22 (applied live to a running user-mode NAT VM)")
	configCmd.Flags().StringVar(&configCPUPinning, "cpu-pinning", "", "Comma-separated host CPU indices to pin the VM's vCPU threads to, e.g. 2,3,4,5,6,7,8,9 (empty clears pinning); applies on the next start")
	configCmd.Flags().StringVar(&configMemory, "memory", "", "Set the VM's memory size, e.g. 10G; applies on the next start")
	configCmd.Flags().IntVar(&configCPUs, "cpus", 0, "Set the VM's vCPU count; applies on the next start")
	configCmd.Flags().StringVar(&configNICMode, "nic-mode", "", "Set the VM's NIC mode: user or bridge; applies on the next start")
	configCmd.Flags().StringVar(&configNICBridge, "nic-bridge", "", "Bridge interface when --nic-mode=bridge (default br0)")
}
