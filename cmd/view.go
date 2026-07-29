package cmd

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/vm"
)

var viewForceSPICE bool

var viewCmd = &cobra.Command{
	Use:               "view <name>",
	Short:             "Open or connect to a running VM's display",
	ValidArgsFunction: completeVMNames,
	Long: `Open the display for a running VM:

  macOS guest (vz) Opens Screen Sharing (VNC) to the guest's own IP.
  GPU passthrough  Prints Moonlight/Sunshine connection instructions.
  SPICE            Opens remote-viewer (must be installed).
  virtio-gpu       Informs the user the display is in the QEMU GTK window.
  --force-spice    Open remote-viewer even on passthrough VMs (headless admin).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		// loadRunningVM reconciles the recorded state against the actual
		// process, unlike a bare LoadState: a crashed VM whose state still
		// says Running would otherwise send the viewer at a stale address.
		cfg, state, err := loadRunningVM(name)
		if err != nil {
			return err
		}

		// macOS guests have no SPICE server: the screen is reached through the
		// guest's own Screen Sharing service, which the guest must have
		// enabled (vee's first-boot provisioning does that when available).
		if state.BackendName() == backend.VZ {
			return viewVZ(cmd, cfg, name)
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
			//nolint:gosec // viewer resolved via LookPath; uri is a vee-constructed spice:// URL.
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

// viewVZ connects to the guest's Screen Sharing service. macOS registers
// Screen Sharing.app as the vnc:// handler, so `open` is enough on the host;
// other hosts cannot run vz VMs at all.
func viewVZ(cmd *cobra.Command, cfg *vm.VMConfig, name string) error {
	if cfg.NIC.MAC == "" {
		return fmt.Errorf("VM %q has no MAC address recorded; cannot resolve its IP", name)
	}
	ip, err := vm.ResolveIPFromMAC(cfg.NIC.MAC)
	if err != nil {
		return fmt.Errorf("could not resolve the guest IP for %q (MAC %s): %w\n"+
			"The guest has not requested a DHCP lease yet — a freshly restored macOS guest takes "+
			"a few minutes on its first boot. Helper log: %s", name, cfg.NIC.MAC, err,
			filepath.Join(prov.Config().StoragePath, name, "vz-helper.log"))
	}

	uri := "vnc://" + ip
	user := cfg.SSHUser
	if user == "" {
		user = "the guest admin account"
	}
	// Probe the screen-sharing port before launching a client. `open` hands
	// the URL to Screen Sharing.app and exits 0 whether or not the guest is
	// reachable, so its exit status proves nothing — and launching blind
	// leaves the user with an opaque "Connection failed" dialog.
	addr := net.JoinHostPort(ip, "5900")
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, dialErr := dialer.DialContext(cmd.Context(), "tcp", addr)
	if dialErr != nil {
		return fmt.Errorf("nothing is listening on %s: %w\n"+
			"The guest's Screen Sharing service is not reachable yet. A freshly restored guest needs "+
			"a few minutes on its first boot, while the provisioning daemon enables the service. "+
			"If it stays unreachable, check that provisioning finished (vee ssh %s, then look for "+
			"/private/var/db/.vee-firstboot-done) — a guest created with --skip-first-boot was never "+
			"provisioned at all.\nHelper log: %s", addr, dialErr, name,
			filepath.Join(prov.Config().StoragePath, name, "vz-helper.log"))
	}
	_ = conn.Close()

	opener, lookErr := exec.LookPath("open")
	if lookErr != nil {
		// No `open` (vz VMs cannot run on such a host anyway): the address is
		// what the user needs, and any VNC client accepts it.
		fmt.Printf("Connect a VNC client to %s (log in as %s)\n", uri, user)
		return nil //nolint:nilerr // missing viewer is not a failure; the address was printed
	}
	fmt.Printf("Opening %s (log in as %s)\n", uri, user)
	//nolint:gosec // opener resolved via LookPath; uri is a vee-constructed vnc:// URL
	launch := exec.CommandContext(cmd.Context(), opener, uri)
	launch.Stderr = os.Stderr
	if err := launch.Run(); err != nil {
		return fmt.Errorf("launch a VNC client for %s: %w (is a vnc:// handler registered?)", uri, err)
	}
	return nil
}

func init() {
	viewCmd.Flags().BoolVar(&viewForceSPICE, "force-spice", false, "Open SPICE viewer even for GPU passthrough VMs")
	rootCmd.AddCommand(viewCmd)
}
