package cmd

import (
	"fmt"
	"os/exec"

	"github.com/Benehiko/vee/internal/vm"
	"github.com/spf13/cobra"
)

var viewForceSPICE bool

var viewCmd = &cobra.Command{
	Use:               "view <name>",
	Short:             "Open or connect to a running VM's display",
	ValidArgsFunction: completeVMNames,
	Long: `Open the display for a running VM:

  GPU passthrough  Prints Moonlight/Sunshine connection instructions.
  SPICE            Opens remote-viewer (must be installed).
  virtio-gpu       Informs the user the display is in the QEMU GTK window.
  --force-spice    Open remote-viewer even on passthrough VMs (headless admin).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		mgr := vm.NewManager(prov)

		cfg, err := mgr.LoadConfig(name)
		if err != nil {
			return fmt.Errorf("load config for %q: %w", name, err)
		}
		state, err := mgr.LoadState(name)
		if err != nil || !state.Running {
			return fmt.Errorf("VM %q is not running", name)
		}

		// GPU passthrough — Sunshine/Moonlight streaming.
		if cfg.GPU.Mode == vm.GPUPassthrough && !viewForceSPICE {
			hostIP := localIP()
			fmt.Printf("VM %q uses GPU passthrough — connect via Moonlight:\n", name)
			fmt.Printf("\n  Host: %s\n  Port: 47989 (Sunshine default)\n\n", hostIP)
			fmt.Printf("Make sure Sunshine is running inside the VM.\n")
			return nil
		}

		// SPICE — open remote-viewer.
		spicePort := state.SPICEPort
		if spicePort == 0 && cfg.SPICE != nil {
			spicePort = cfg.SPICE.Port
		}
		if spicePort > 0 {
			uri := fmt.Sprintf("spice://localhost:%d", spicePort)
			fmt.Printf("Opening %s\n", uri)
			viewer, err := exec.LookPath("remote-viewer")
			if err != nil {
				return fmt.Errorf("remote-viewer not found; install virt-viewer: %w", err)
			}
			return exec.Command(viewer, uri).Start()
		}

		// virtio-gpu / no display configured.
		if cfg.GPU.Mode == vm.GPUVirtio {
			fmt.Printf("VM %q uses virtio-gpu — display is in the QEMU GTK window.\n", name)
			return nil
		}

		return fmt.Errorf("VM %q has no SPICE port configured and no GPU display; use --foreground to run it in the terminal", name)
	},
}

func init() {
	viewCmd.Flags().BoolVar(&viewForceSPICE, "force-spice", false, "Open SPICE viewer even for GPU passthrough VMs")
	rootCmd.AddCommand(viewCmd)
}
