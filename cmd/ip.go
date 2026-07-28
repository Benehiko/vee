package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/vm"
)

var ipCmd = &cobra.Command{
	Use:               "ip <name>",
	Short:             "Show a running VM's IP addresses (guest agent, or host lease/ARP tables by MAC)",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, state, err := loadRunningVM(name)
		if err != nil {
			return err
		}
		if state.QGASocket == "" {
			// No guest agent. Only host-visible guests can be resolved by
			// MAC (vz NAT and bridge mode); user-mode slirp guests never
			// appear in host lease/neighbour tables, so falling back there
			// would return nothing — or worse, a stale lease left by a
			// deleted same-named VM.
			hostVisible := state.BackendName() == backend.VZ || cfg.NIC.Mode == "bridge"
			if hostVisible && cfg.NIC.MAC != "" {
				ip, resolveErr := vm.ResolveIPFromMAC(cfg.NIC.MAC)
				if resolveErr != nil {
					return fmt.Errorf("VM %q has no guest agent and MAC-based IP resolution failed: %w", name, resolveErr)
				}
				// Bare IP on stdout so $(vee ip <name>) stays scriptable.
				fmt.Println(ip)
				return nil
			}
			return fmt.Errorf("VM %q was not started with guest agent support; recreate with a template that enables guest_agent", name)
		}

		client, closeClient, err := openQGAClient(cmd.Context(), state.QGASocket, 5*time.Second)
		if err != nil {
			return err
		}
		defer closeClient()

		ifaces, err := client.GuestNetworkGetInterfaces()
		if err != nil {
			return fmt.Errorf("get interfaces: %w", err)
		}

		if cfg.Hostname != "" {
			fmt.Printf("hostname: %s\n\n", cfg.Hostname)
		}
		fmt.Printf("%-12s %-20s %s\n", "NIC", "MAC", "ADDRESSES")
		fmt.Println(strings.Repeat("-", 60))
		for _, iface := range ifaces {
			var addrs []string
			for _, a := range iface.IPAddresses {
				addrs = append(addrs, fmt.Sprintf("%s/%d", a.IPAddress, a.Prefix))
			}
			fmt.Printf("%-12s %-20s %s\n", iface.Name, iface.HardwareAddress, strings.Join(addrs, "  "))
		}
		return nil
	},
}
