package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Benehiko/vee/qemu"
	"github.com/Benehiko/vee/vm"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:               "status <name>",
	Short:             "Show detailed status of a VM",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		mgr := vm.NewManager(prov)
		entries, err := mgr.List()
		if err != nil {
			return err
		}

		var entry *vm.ListEntry
		for _, e := range entries {
			if e.Config.Name == name {
				entry = e
				break
			}
		}
		if entry == nil {
			return fmt.Errorf("VM %q not found", name)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

		printRow := func(k, v string) {
			_, _ = fmt.Fprintf(w, "%s\t%s\n", k, v)
		}

		printRow("name", entry.Config.Name)
		printRow("template", entry.Config.Template)
		printRow("memory", entry.Config.Memory)
		printRow("cpus", fmt.Sprintf("%d", entry.Config.CPUs))

		if entry.State.Running {
			printRow("status", "running")
			printRow("pid", fmt.Sprintf("%d", entry.State.PID))
			if entry.State.StartedAt != nil && !entry.State.StartedAt.IsZero() {
				uptime := time.Since(*entry.State.StartedAt).Truncate(time.Second)
				printRow("uptime", uptime.String())
			}
			if entry.State.SPICEPort > 0 {
				printRow("spice", fmt.Sprintf(":%d", entry.State.SPICEPort))
			}
			if entry.State.SSHPort > 0 {
				printRow("ssh", fmt.Sprintf("127.0.0.1:%d", entry.State.SSHPort))
			}
		} else {
			printRow("status", "stopped")
		}

		_ = w.Flush()

		// QGA section — only if VM is running and has a QGA socket.
		if !entry.State.Running || entry.State.QGASocket == "" {
			return nil
		}

		fmt.Println()
		client, err := qemu.NewQGAClient(entry.State.QGASocket, 3*time.Second)
		if err != nil {
			fmt.Printf("guest-agent: unavailable (%v)\n", err)
			return nil
		}
		defer func() { _ = client.Close() }()

		if err := client.GuestPing(); err != nil {
			fmt.Printf("guest-agent: not responding (%v)\n", err)
			return nil
		}

		fmt.Println("guest-agent: connected")
		fmt.Println()

		w2 := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w2, "INTERFACE\tIP\tMAC")

		ifaces, err := client.GuestNetworkGetInterfaces()
		if err == nil {
			for _, iface := range ifaces {
				if iface.Name == "lo" {
					continue
				}
				var ips []string
				for _, addr := range iface.IPAddresses {
					if addr.IPAddressType == "ipv4" {
						ips = append(ips, fmt.Sprintf("%s/%d", addr.IPAddress, addr.Prefix))
					}
				}
				ipStr := strings.Join(ips, ", ")
				if ipStr == "" {
					ipStr = "-"
				}
				_, _ = fmt.Fprintf(w2, "%s\t%s\t%s\n", iface.Name, ipStr, iface.HardwareAddress)
			}
		}
		_ = w2.Flush()

		// Hostname and OS info via guest-exec.
		hostname, _, _, herr := client.RunCommand("/bin/hostname", nil)
		if herr == nil {
			fmt.Printf("\nhostname: %s", hostname)
		}

		osRelease, _, _, oerr := client.RunCommand("/bin/sh", []string{"-c", "grep PRETTY_NAME /etc/os-release | cut -d= -f2 | tr -d '\"'"})
		if oerr == nil {
			fmt.Printf("os:       %s", osRelease)
		}

		uptime, _, _, uerr := client.RunCommand("/usr/bin/uptime", []string{"-p"})
		if uerr == nil {
			fmt.Printf("uptime:   %s", uptime)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
