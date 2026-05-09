package cmd

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/Benehiko/vee/gpu"
	"github.com/spf13/cobra"
)

const (
	checkPass = "PASS"
	checkFail = "FAIL"
	checkSkip = "SKIP"
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
		_, _ = fmt.Fprintln(w, "GROUP\tADDRESS\tNAME\tCLASS\tDRIVER\tGPU")
		for _, g := range groups {
			for _, d := range g.Devices {
				gpuMark := ""
				if d.IsGPU {
					gpuMark = "yes"
				}
				name := gpu.LookupDeviceName(d.Vendor, d.Device)
				if name == "" {
					name = d.Vendor + ":" + d.Device
				}
				_, _ = fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n",
					g.ID, d.Address, name, d.Class, d.Driver, gpuMark)
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

var gpuStatusMemory string

var gpuStatusCmd = &cobra.Command{
	Use:               "status <pci-addr>",
	Short:             "Check VFIO passthrough readiness for a PCI device",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: cobra.NoFileCompletions,
	RunE: func(cmd *cobra.Command, args []string) error {
		r := gpu.PreflightCheck(args[0], gpuStatusMemory)

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "CHECK\tSTATUS\tDETAIL")

		check := func(name, detail string, err error) {
			status := checkPass
			if err != nil {
				status = checkFail
				detail = err.Error()
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", name, status, detail)
		}
		skipIf := func(name, detail string, skip bool, err error) {
			if skip {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", name, checkSkip, detail)
				return
			}
			check(name, detail, err)
		}

		check("driver",
			fmt.Sprintf("bound to %s", r.Driver),
			r.Errors["driver"])

		check("iommu_group",
			fmt.Sprintf("group %d", r.IOMMUGroup),
			r.Errors["iommu_group"])

		for _, peer := range r.GroupPeers {
			name := gpu.LookupDeviceName(peer.Vendor, peer.Device)
			if name == "" {
				name = peer.Vendor + ":" + peer.Device
			}
			check("iommu_group_peer_"+peer.Address,
				fmt.Sprintf("%s driver=%s", name, peer.Driver),
				r.Errors["iommu_group_peer_"+peer.Address])
		}

		skipIf("vfio_dev",
			fmt.Sprintf("%s exists", r.VFIODevPath),
			r.IOMMUGroup < 0,
			r.Errors["vfio_dev"])

		skipIf("vfio_access",
			fmt.Sprintf("%s is accessible", r.VFIODevPath),
			r.IOMMUGroup < 0 || r.Errors["vfio_dev"] != nil,
			r.Errors["vfio_access"])

		const unlimited = ^uint64(0)
		softStr := gpu.FormatBytes(r.MemlockSoftBytes)
		hardStr := gpu.FormatBytes(r.MemlockHardBytes)
		requiredStr := ""
		if r.MemlockRequiredBytes > 0 {
			requiredStr = fmt.Sprintf(" (need %s for VM RAM)", gpu.FormatBytes(r.MemlockRequiredBytes))
		}
		skipIf("memlock",
			fmt.Sprintf("soft=%s hard=%s%s", softStr, hardStr, requiredStr),
			gpuStatusMemory == "" && r.MemlockSoftBytes == unlimited,
			r.Errors["memlock"])

		_ = w.Flush()

		if !r.OK() {
			return fmt.Errorf("preflight checks failed — fix the issues above before starting the VM")
		}
		fmt.Println("\nAll checks passed.")
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
	gpuStatusCmd.Flags().StringVar(&gpuStatusMemory, "memory", "", "VM RAM size (e.g. 16G) to check memlock sufficiency")
	gpuUnbindCmd.Flags().StringVar(&unbindOriginalDriver, "driver", "", "Original driver to rebind to (auto-detected if omitted)")
	gpuCmd.AddCommand(gpuListCmd, gpuBindCmd, gpuUnbindCmd, gpuStatusCmd)
	rootCmd.AddCommand(gpuCmd)
}
