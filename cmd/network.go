package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/vm"
)

var (
	networkJSON       bool
	networkSkipEgress bool
)

var networkCmd = &cobra.Command{
	Use:   "network <name>",
	Short: "Show a running VM's network state: firewall, VPN, DNS, routes, and egress checks",
	Long: `Show a running VM's network state from both sides of the VM boundary.

The host section reports what the host can see of the guest (NIC mode, ARP
visibility, SSH and guest-agent reachability). The guest section reports what
the guest sees of its own networking (interfaces, default route, DNS servers,
ufw, VPN tunnel) probed via the guest agent with an SSH fallback.

For VPN-configured VMs (the torrent template) the report includes pass/fail
checks: kill-switch enabled, default route through the tunnel, DNS not
leaking to the LAN resolver, and the guest's egress IP differing from the
host's public IP. The egress checks make one HTTPS request to Cloudflare's
trace endpoint from each side and one DNS query from the guest; disable them
with --skip-egress.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		mgr := vm.NewManager(prov)
		report, err := mgr.QueryNetwork(cmd.Context(), name, vm.NetworkOptions{
			SkipEgress: networkSkipEgress,
		})
		if err != nil {
			return err
		}
		if networkJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(report)
		}
		printNetworkReport(report)
		return nil
	},
}

func printNetworkReport(r *vm.NetworkReport) {
	fmt.Println("host:")
	nic := r.Host.NICMode
	if r.Host.MAC != "" {
		nic += " (" + r.Host.MAC + ")"
	}
	printNetFact("nic", nic)
	if r.Host.ResolvedIP != "" {
		printNetFact("ip (arp)", r.Host.ResolvedIP)
	}
	if r.Host.SSHPort > 0 {
		printNetFact("ssh", fmt.Sprintf("127.0.0.1:%d", r.Host.SSHPort))
	}
	printNetFact("qga", map[bool]string{true: "available", false: "not configured"}[r.Host.QGA])
	if r.Host.PublicIP != "" {
		printNetFact("public ip", r.Host.PublicIP)
	}

	fmt.Println("guest:")
	if len(r.Guest.Interfaces) > 0 {
		var parts []string
		for _, iface := range r.Guest.Interfaces {
			if iface.Name == "lo" {
				continue
			}
			var addrs []string
			for _, a := range iface.IPAddresses {
				if a.IPAddressType == "ipv4" {
					addrs = append(addrs, fmt.Sprintf("%s/%d", a.IPAddress, a.Prefix))
				}
			}
			if len(addrs) > 0 {
				parts = append(parts, fmt.Sprintf("%s %s", iface.Name, strings.Join(addrs, " ")))
			}
		}
		printNetFact("interfaces", strings.Join(parts, ", "))
	}
	if r.Guest.DefaultRoute != "" {
		printNetFact("route", "default "+r.Guest.DefaultRoute)
	}
	if r.Guest.PolicyRoute != "" {
		printNetFact("policy route", r.Guest.PolicyRoute)
	}
	if len(r.Guest.DNSServers) > 0 {
		printNetFact("dns", strings.Join(r.Guest.DNSServers, ", "))
	}
	if r.Guest.UFW.Available {
		printNetFact("ufw", fmt.Sprintf("%s, outgoing=%s (%d rules)",
			map[bool]string{true: "active", false: "inactive"}[r.Guest.UFW.Active],
			r.Guest.UFW.DefaultOutgoing, r.Guest.UFW.RuleCount))
	}
	if r.Guest.VPN.Provider != "" {
		v := r.Guest.VPN
		desc := v.Provider
		switch {
		case !v.Available:
			desc += " state unavailable"
		case v.Connected:
			desc += " connected"
		default:
			desc += " NOT connected"
		}
		if v.Endpoint != "" {
			desc += " (" + v.Endpoint + ")"
		}
		if v.Killswitch != "" {
			desc += ", killswitch=" + v.Killswitch
		}
		printNetFact("vpn", desc)
	}
	if r.Guest.EgressIP != "" {
		printNetFact("egress", r.Guest.EgressIP)
	}
	if r.Guest.DNSEgressIP != "" {
		printNetFact("dns egress", r.Guest.DNSEgressIP)
	}

	printNetChecks(append(append([]vm.NetCheck{}, r.Host.Checks...), r.Guest.Checks...))
}

func printNetFact(name, value string) {
	fmt.Printf("  %-12s %s\n", name, value)
}

func printNetChecks(checks []vm.NetCheck) {
	if len(checks) == 0 {
		return
	}
	counts := map[string]int{}
	for _, c := range checks {
		counts[c.Status]++
	}
	fmt.Printf("\nchecks: %d passed, %d failed, %d info, %d unavailable\n\n",
		counts[vm.NetCheckPass], counts[vm.NetCheckFail],
		counts[vm.NetCheckInfo], counts[vm.NetCheckUnavailable])

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "CHECK\tSTATUS\tDETAIL")
	for _, c := range checks {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", c.Name, strings.ToUpper(c.Status), c.Detail)
	}
	_ = w.Flush()
}

func init() {
	networkCmd.Flags().BoolVar(&networkJSON, "json", false, "output the full report as JSON")
	networkCmd.Flags().BoolVar(&networkSkipEgress, "skip-egress", false, "skip external egress-IP lookups (no HTTPS/DNS requests leave the host or guest)")
	rootCmd.AddCommand(networkCmd)
}
