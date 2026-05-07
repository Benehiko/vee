package cmd

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/Benehiko/vee/gpu"
	"github.com/spf13/cobra"
)

var gpuCmd = &cobra.Command{
	Use:   "gpu",
	Short: "Manage GPU devices for VM passthrough",
}

var gpuListCmd = &cobra.Command{
	Use:   "list",
	Short: "List IOMMU groups and GPU devices",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		groups, err := gpu.ListIOMMUGroups()
		if err != nil {
			return err
		}

		sort.Slice(groups, func(i, j int) bool {
			return groups[i].ID < groups[j].ID
		})

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "GROUP\tADDRESS\tVENDOR\tDEVICE\tCLASS\tDRIVER\tGPU")
		for _, g := range groups {
			for _, d := range g.Devices {
				gpuMark := ""
				if d.IsGPU {
					gpuMark = "yes"
				}
				_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
					g.ID, d.Address, d.Vendor, d.Device, d.Class, d.Driver, gpuMark)
			}
		}
		return w.Flush()
	},
}

var gpuBindCmd = &cobra.Command{
	Use:   "bind <pci-addr>",
	Short: "Bind a PCI device to the vfio-pci driver (requires root)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		addr := args[0]
		fmt.Printf("Binding %s to vfio-pci...\n", addr)
		if err := gpu.BindVFIO(addr); err != nil {
			return err
		}
		fmt.Printf("OK — %s is now bound to vfio-pci\n", addr)
		return nil
	},
}

var unbindOriginalDriver string

var gpuUnbindCmd = &cobra.Command{
	Use:   "unbind <pci-addr>",
	Short: "Remove a PCI device from vfio-pci (requires root)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		addr := args[0]
		if unbindOriginalDriver == "" {
			unbindOriginalDriver = gpu.CurrentDriver(addr)
			if unbindOriginalDriver == "vfio-pci" {
				// Can't rebind to vfio-pci itself.
				unbindOriginalDriver = ""
			}
		}
		fmt.Printf("Unbinding %s from vfio-pci", addr)
		if unbindOriginalDriver != "" {
			fmt.Printf(", rebinding to %s", unbindOriginalDriver)
		}
		fmt.Println("...")
		if err := gpu.UnbindVFIO(addr, unbindOriginalDriver); err != nil {
			return err
		}
		fmt.Printf("OK\n")
		return nil
	},
}

func init() {
	gpuUnbindCmd.Flags().StringVar(&unbindOriginalDriver, "driver", "", "Original driver to rebind to (auto-detected if omitted)")
	gpuCmd.AddCommand(gpuListCmd, gpuBindCmd, gpuUnbindCmd)
	rootCmd.AddCommand(gpuCmd)
}
