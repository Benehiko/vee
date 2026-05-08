package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/Benehiko/vee/qemu"
	"github.com/spf13/cobra"
)

var ipCmd = &cobra.Command{
	Use:               "ip <name>",
	Short:             "Show network interfaces and IP addresses inside a running VM",
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

		client, err := qemu.NewQGAClient(state.QGASocket, 5*time.Second)
		if err != nil {
			return fmt.Errorf("connect to guest agent: %w", err)
		}
		defer func() { _ = client.Close() }()

		ifaces, err := client.GuestNetworkGetInterfaces()
		if err != nil {
			return fmt.Errorf("get interfaces: %w", err)
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
