package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/vm"
)

// tunnelListStatuses gathers background tunnels for `--list`. It prefers the
// daemon, which knows which listeners are actually up; with no daemon running
// it falls back to the registry so the user still sees what is registered and
// why nothing is serving it.
func tunnelListStatuses(ctx context.Context, reload bool) ([]vm.TunnelStatus, bool, error) {
	mgr := vm.NewManager(prov)
	statuses, daemonUp, err := mgr.TunnelsViaDaemon(ctx, reload)
	if daemonUp && err != nil {
		return nil, true, err
	}
	if daemonUp {
		return statuses, true, nil
	}

	reason := "daemon not running"
	if errors.Is(err, vm.ErrDaemonTooOld) {
		reason = "daemon needs restarting"
	}

	specs, listErr := mgr.Tunnels().List()
	if listErr != nil {
		return nil, false, listErr
	}
	out := make([]vm.TunnelStatus, 0, len(specs))
	for _, s := range specs {
		out = append(out, vm.TunnelStatus{
			VM:       s.VM,
			Service:  s.Service,
			Hostname: s.VHost(),
			Bind:     s.BindAddr(),
			Port:     s.Port,
			Protocol: s.Protocol,
			Reason:   reason,
		})
	}
	// err is carried through (not treated as failure) so the caller can tell
	// an absent daemon from an out-of-date one when printing the hint.
	return out, false, err
}

// daemonHint tells the user how to get a daemon that serves tunnels, tailored
// to whether one is absent or merely out of date.
func daemonHint(err error) string {
	if errors.Is(err, vm.ErrDaemonTooOld) {
		return "The running vee daemon predates background tunnels.\nRestart it with: systemctl restart vee  (Linux)"
	}
	return "The vee daemon is not running, so no tunnel is currently served.\nStart it with: vee daemon install  (or: vee daemon)"
}

// printTunnelList renders the background tunnel table.
func printTunnelList(statuses []vm.TunnelStatus, daemonUp bool, daemonErr error) error {
	if len(statuses) == 0 {
		fmt.Println("No background tunnels registered.")
		fmt.Println()
		fmt.Println("Register one with: vee tunnel <vm> <service> --background")
		return nil
	}

	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].VM != statuses[j].VM {
			return statuses[i].VM < statuses[j].VM
		}
		return statuses[i].Service < statuses[j].Service
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "VM\tSERVICE\tBIND\tPORT\tSTATE\tURL")
	for _, s := range statuses {
		state := "inactive"
		if s.Active {
			state = "active"
		}
		if s.Reason != "" {
			state += " (" + s.Reason + ")"
		}
		port := "-"
		if s.Port > 0 {
			port = fmt.Sprintf("%d", s.Port)
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.VM, s.Service, s.Bind, port, state, tunnelStatusURL(s))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	if !daemonUp {
		fmt.Println()
		fmt.Println(daemonHint(daemonErr))
	}
	return nil
}

// tunnelStatusURL renders the address a tunnel is reachable at. LAN-bound
// HTTP tunnels get their published vhost name, which is the address the user
// is meant to use; everything else gets host:port.
func tunnelStatusURL(s vm.TunnelStatus) string {
	if s.Port <= 0 {
		return "-"
	}
	var scheme string
	switch s.Protocol {
	case vm.ServiceHTTPS:
		scheme = "https://"
	case vm.ServiceHTTP:
		scheme = "http://"
	case vm.ServiceSPICE:
		scheme = "spice://"
	default:
		// Raw TCP has no scheme; the address is printed bare.
		scheme = ""
	}

	host := "localhost"
	if s.Bind == vm.BindHost {
		host = tunnelHostname()
		if scheme != "" && s.Active {
			// Published under its own name by the daemon's vhost router.
			if label := vm.SanitizeHostname(s.Hostname); label != "" {
				return fmt.Sprintf("%s%s.%s/", scheme, label, host)
			}
		}
	}
	if scheme == "" {
		return fmt.Sprintf("%s:%d", host, s.Port)
	}
	return fmt.Sprintf("%s%s:%d", scheme, host, s.Port)
}

// tunnelHostname is the machine's short hostname, matching the suffix the
// daemon's router publishes names under.
func tunnelHostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "localhost"
	}
	label, _, _ := strings.Cut(h, ".")
	return strings.ToLower(label)
}

// registerBackgroundTunnel writes the tunnel to the registry and asks a
// running daemon to reconcile immediately, so the printed URL works right
// away rather than after the next poll tick.
func registerBackgroundTunnel(ctx context.Context, spec vm.TunnelSpec) error {
	mgr := vm.NewManager(prov)
	if err := mgr.Tunnels().Add(spec); err != nil {
		return err
	}

	statuses, daemonUp, err := mgr.TunnelsViaDaemon(ctx, true)
	if !daemonUp {
		fmt.Printf("Registered background tunnel %s/%s (not served yet).\n", spec.VM, spec.Service)
		fmt.Println(daemonHint(err))
		return nil
	}
	if err != nil {
		return fmt.Errorf("daemon could not reload tunnels: %w", err)
	}

	for _, s := range statuses {
		if s.VM != spec.VM || s.Service != spec.Service {
			continue
		}
		if !s.Active {
			fmt.Printf("Registered background tunnel %s/%s (not active: %s).\n",
				spec.VM, spec.Service, s.Reason)
			fmt.Println("It will be established automatically once the VM is running.")
			return nil
		}
		fmt.Printf("Background tunnel %s/%s active on %s:%d → guest:%d\n",
			spec.VM, spec.Service, s.Bind, s.Port, s.GuestPort)
		fmt.Println(tunnelStatusURL(s))
		if s.Bind == vm.BindHost {
			fmt.Println()
			fmt.Printf("Reachable from any device on the LAN. For the name above to resolve,\n")
			fmt.Printf("publish %s.%s in your DNS (see docs/tunnels.md).\n",
				vm.SanitizeHostname(s.Hostname), tunnelHostname())
		}
		return nil
	}
	fmt.Printf("Registered background tunnel %s/%s.\n", spec.VM, spec.Service)
	return nil
}

// unregisterBackgroundTunnel removes a tunnel from the registry and asks the
// daemon to tear the listener down.
func unregisterBackgroundTunnel(ctx context.Context, vmName, service string) error {
	mgr := vm.NewManager(prov)
	removed, err := mgr.Tunnels().Remove(vmName, service)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("no background tunnel registered for %s/%s", vmName, service)
	}
	// Best-effort: with no daemon there is no listener to stop either.
	if _, daemonUp, err := mgr.TunnelsViaDaemon(ctx, true); daemonUp && err != nil {
		return fmt.Errorf("daemon could not reload tunnels: %w", err)
	}
	fmt.Printf("Removed background tunnel %s/%s.\n", vmName, service)
	return nil
}

// confirmHostBind warns before publishing a service to the LAN and asks for
// confirmation. vee adds no authentication of its own, so the guest app's own
// login is the only thing standing between the service and every device on
// the network — that is worth stating explicitly rather than burying in docs.
func confirmHostBind(vmName string, s resolvedService, assumeYes bool) bool {
	if assumeYes {
		return true
	}
	fmt.Fprintf(os.Stderr,
		"WARNING: binding %s:%d exposes %s/%s to every device on your LAN.\n",
		vm.BindHost, s.Port, vmName, s.Name)
	fmt.Fprintf(os.Stderr,
		"vee adds no authentication — the guest application's own login is the only protection.\n")
	return confirm("Continue?", false)
}

// backgroundService registers a service as a background tunnel. Unlike the
// foreground path it does not block: the daemon owns the listener, so the
// command returns as soon as the registration is reconciled.
func backgroundService(cmd *cobra.Command, cfg *vm.VMConfig, s resolvedService) error {
	if s.Protocol == vm.ServiceSPICE {
		return fmt.Errorf("SPICE services cannot be backgrounded — QEMU already binds the SPICE port on the host")
	}
	if s.Port <= 0 {
		return fmt.Errorf("service %q has no port assigned yet (has the VM started?)", s.Name)
	}

	bind := vm.BindLoopback
	if tunnelBindHost {
		if !confirmHostBind(cfg.Name, s, tunnelYes) {
			return fmt.Errorf("aborted")
		}
		bind = vm.BindHost
	}

	hostname := tunnelHostnameOverride
	if hostname == "" {
		hostname = s.Name
	}
	if label := vm.SanitizeHostname(hostname); label == "" {
		return fmt.Errorf("hostname %q contains no characters usable in a DNS name", hostname)
	}

	// Reuse the port already recorded for this tunnel, so re-running the
	// command does not move a published service to a new port and break
	// whatever points at it.
	port := 0
	if existing, err := vm.NewManager(prov).Tunnels().List(); err == nil {
		key := vm.TunnelSpec{VM: cfg.Name, Service: s.Name}.Key()
		for _, e := range existing {
			if e.Key() == key {
				port = e.Port
				break
			}
		}
	}

	return registerBackgroundTunnel(cmd.Context(), vm.TunnelSpec{
		VM:       cfg.Name,
		Service:  s.Name,
		Bind:     bind,
		Port:     port,
		Hostname: hostname,
		Protocol: s.Protocol,
	})
}
