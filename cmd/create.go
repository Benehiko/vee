package cmd

import (
	"fmt"
	"time"

	"github.com/Benehiko/vee/vm"
	"github.com/spf13/cobra"
)

var (
	createTemplate  string
	createMemory    string
	createCPUs      int
	createDisk      string
	createNicMode   string
	createNicBridge string
	createSpicePort int
	createUEFI      bool
)

var createCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new VM",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		conf := prov.Config()

		cfg := &vm.VMConfig{
			Name:     name,
			Template: createTemplate,
			Memory:   createMemory,
			CPUs:     createCPUs,
			Sockets:  1,
			Cores:    createCPUs,
			Threads:  1,
			CPUModel: conf.DefaultCPUModel,
			NIC: vm.NICConfig{
				Mode:   createNicMode,
				Bridge: createNicBridge,
				Model:  "virtio-net-pci",
			},
			GPU: vm.GPUConfig{Mode: vm.GPUNone},
			UEFI: vm.UEFIConfig{
				Enabled: createUEFI,
			},
			CreatedAt: time.Now(),
		}

		if createDisk != "" {
			cfg.Disks = append(cfg.Disks, vm.DiskConfig{
				Size:      createDisk,
				Format:    "qcow2",
				Interface: "virtio",
				Media:     "disk",
				Cache:     "none",
			})
		}

		if createSpicePort > 0 {
			cfg.SPICE = &vm.SPICEConfig{
				Port:             createSpicePort,
				DisableTicketing: true,
			}
		}

		mgr := vm.NewManager(prov)
		if err := mgr.Create(cmd.Context(), cfg); err != nil {
			return err
		}
		fmt.Printf("Created VM %q\n", name)
		return nil
	},
}

func init() {
	createCmd.Flags().StringVar(&createTemplate, "template", "ubuntu-server", "VM template")
	createCmd.Flags().StringVar(&createMemory, "memory", "2G", "Memory size (e.g. 4G)")
	createCmd.Flags().IntVar(&createCPUs, "cpus", 2, "Number of vCPUs")
	createCmd.Flags().StringVar(&createDisk, "disk", "20G", "Primary disk size (e.g. 50G)")
	createCmd.Flags().StringVar(&createNicMode, "nic-mode", "user", "NIC mode: bridge or user")
	createCmd.Flags().StringVar(&createNicBridge, "nic-bridge", "br0", "Bridge interface (when nic-mode=bridge)")
	createCmd.Flags().IntVar(&createSpicePort, "spice-port", 0, "SPICE port (0 = disabled)")
	createCmd.Flags().BoolVar(&createUEFI, "uefi", false, "Enable UEFI boot (OVMF)")
}
