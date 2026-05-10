package cmd

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/Benehiko/vee/internal/vm"
	"github.com/spf13/cobra"
)

var tunnelCmd = &cobra.Command{
	Use:   "tunnel <name> [service]",
	Short: "Connect to a VM service (opens browser for HTTP/S, prints URL for others)",
	Long: `Show and connect to services exposed by a running VM.

Without a service argument, lists all available services and their connection URLs.
With a service argument, immediately opens or connects to that service.

HTTP/HTTPS services open in the default browser.
SPICE services print a spice:// URL.
TCP services print the forwarded address.

For bridge-mode VMs, opens a direct TCP proxy — no SSH needed.
For user-mode VMs, opens an SSH -L tunnel.`,
	Args:              cobra.RangeArgs(1, 2),
	ValidArgsFunction: completeTunnelArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, state, err := loadRunningVM(name)
		if err != nil {
			return err
		}

		services := resolvedServices(cfg, state)
		if len(services) == 0 {
			return fmt.Errorf("VM %q has no declared services", name)
		}

		if len(args) == 1 {
			return printServiceMenu(cfg, services)
		}

		svcName := args[1]
		for _, s := range services {
			if s.Name == svcName {
				return connectService(cmd, cfg, state, s)
			}
		}
		return fmt.Errorf("unknown service %q — run 'vee tunnel %s' to list available services", svcName, name)
	},
}

// resolvedService is a ServiceEntry with its connection port resolved for the
// current VM state (SPICE port comes from state, others from config).
type resolvedService struct {
	vm.ServiceEntry
}

func resolvedServices(cfg *vm.VMConfig, state *vm.VMState) []resolvedService {
	var out []resolvedService
	for _, s := range cfg.Services {
		rs := resolvedService{s}
		// SPICE port lives in state after first start.
		if s.Protocol == vm.ServiceSPICE {
			if state.SPICEPort > 0 {
				rs.Port = state.SPICEPort
			} else if cfg.SPICE != nil {
				rs.Port = cfg.SPICE.Port
			}
		}
		out = append(out, rs)
	}
	return out
}

func printServiceMenu(cfg *vm.VMConfig, services []resolvedService) error {
	fmt.Printf("%-16s  %-10s  %s\n", "SERVICE", "PROTOCOL", "CONNECTION")
	fmt.Println(strings.Repeat("─", 60))
	for _, s := range services {
		fmt.Printf("%-16s  %-10s  %s\n", s.Name, s.Protocol, serviceURL(cfg, s))
	}
	fmt.Println()
	fmt.Println("Run: vee tunnel <vm> <service>  to connect")
	return nil
}

func serviceURL(cfg *vm.VMConfig, s resolvedService) string {
	// SPICE is always bound on the host by QEMU — show direct URL.
	if s.Protocol == vm.ServiceSPICE {
		return fmt.Sprintf("spice://localhost:%d", s.Port)
	}
	// user-mode with hostfwd — port is already on the host.
	if cfg.NIC.Mode == "user" {
		if hostPort := findHostFwd(cfg.NIC.HostFwds, s.Port); hostPort > 0 {
			switch s.Protocol {
			case vm.ServiceHTTP:
				return fmt.Sprintf("http://localhost:%d", hostPort)
			case vm.ServiceHTTPS:
				return fmt.Sprintf("https://localhost:%d", hostPort)
			default:
				return fmt.Sprintf("localhost:%d", hostPort)
			}
		}
	}
	// Bridge / no hostfwd — a proxy will be opened on a random local port.
	switch s.Protocol {
	case vm.ServiceHTTP:
		return fmt.Sprintf("http://localhost:<proxy> → guest:%d", s.Port)
	case vm.ServiceHTTPS:
		return fmt.Sprintf("https://localhost:<proxy> → guest:%d", s.Port)
	default:
		return fmt.Sprintf("localhost:<proxy> → guest:%d", s.Port)
	}
}

func connectService(cmd *cobra.Command, cfg *vm.VMConfig, state *vm.VMState, s resolvedService) error {
	localPort, err := freeLocalPort()
	if err != nil {
		return fmt.Errorf("find free local port: %w", err)
	}

	// SPICE: for bridge-mode VMs the SPICE port is already bound on the host
	// (QEMU binds it). No tunnel needed — just print the spice:// URL.
	if s.Protocol == vm.ServiceSPICE {
		port := s.Port
		if port == 0 {
			return fmt.Errorf("SPICE port not yet assigned (has the VM started?)")
		}
		url := fmt.Sprintf("spice://localhost:%d", port)
		fmt.Println(url)
		fmt.Println("Open with: remote-viewer", url)
		return nil
	}

	// For user-mode VMs with HostFwds the port is already on the host.
	// Check if this service port is already forwarded.
	if cfg.NIC.Mode == "user" {
		if hostPort := findHostFwd(cfg.NIC.HostFwds, s.Port); hostPort > 0 {
			return openOrPrint(s, hostPort)
		}
	}

	// Otherwise open a tunnel.
	switch {
	case cfg.NIC.Mode == "bridge" || (cfg.NIC.Mode == "" && state.SSHPort == 0):
		vmIP, resolveErr := tunnelResolveIP(cfg, state)
		if resolveErr != nil {
			return resolveErr
		}
		url := localServiceURL(s, localPort)
		fmt.Printf("tunnelling localhost:%d → %s:%d\n", localPort, cfg.Name, s.Port)
		fmt.Println(url)
		maybeBrowser(s, url)
		return runTCPProxy(cfg.Name, localPort, vmIP, s.Port)

	case state.SSHPort > 0:
		url := localServiceURL(s, localPort)
		fmt.Printf("tunnelling localhost:%d → %s:%d\n", localPort, cfg.Name, s.Port)
		fmt.Println(url)
		maybeBrowser(s, url)
		return runSSHTunnel(cfg.Name, localPort, "127.0.0.1", state.SSHPort, s.Port, cfg)

	default:
		return fmt.Errorf("VM %q: cannot determine tunnel method (not bridge, no SSH port)", cfg.Name)
	}
}

// findHostFwd returns the host port for a forwarded guest port, or 0 if none.
// HostFwds format: "tcp:127.0.0.1:<hostPort>-:<guestPort>"
func findHostFwd(fwds []string, guestPort int) int {
	guestStr := strconv.Itoa(guestPort)
	for _, fwd := range fwds {
		// format: proto:hostAddr:hostPort-:guestPort
		parts := strings.SplitN(fwd, "-:", 2)
		if len(parts) != 2 || parts[1] != guestStr {
			continue
		}
		// hostPart = "tcp:127.0.0.1:hostPort"
		hostPart := strings.SplitN(parts[0], ":", 3)
		if len(hostPart) == 3 {
			if p, err := strconv.Atoi(hostPart[2]); err == nil {
				return p
			}
		}
	}
	return 0
}

func localServiceURL(s resolvedService, localPort int) string {
	switch s.Protocol {
	case vm.ServiceHTTP:
		return fmt.Sprintf("http://localhost:%d", localPort)
	case vm.ServiceHTTPS:
		return fmt.Sprintf("https://localhost:%d", localPort)
	case vm.ServiceSPICE:
		return fmt.Sprintf("spice://localhost:%d", localPort)
	default:
		return fmt.Sprintf("localhost:%d", localPort)
	}
}

func openOrPrint(s resolvedService, port int) error {
	url := ""
	switch s.Protocol {
	case vm.ServiceHTTP:
		url = fmt.Sprintf("http://localhost:%d", port)
	case vm.ServiceHTTPS:
		url = fmt.Sprintf("https://localhost:%d", port)
	case vm.ServiceSPICE:
		url = fmt.Sprintf("spice://localhost:%d", port)
		fmt.Println(url)
		fmt.Println("Open with: remote-viewer", url)
		return nil
	default:
		fmt.Printf("localhost:%d\n", port)
		return nil
	}
	fmt.Println(url)
	maybeBrowser(s, url)
	return nil
}

func maybeBrowser(s resolvedService, url string) {
	if s.Protocol != vm.ServiceHTTP && s.Protocol != vm.ServiceHTTPS {
		return
	}
	for _, bin := range []string{"xdg-open", "open"} {
		if path, err := exec.LookPath(bin); err == nil {
			_ = exec.Command(path, url).Start()
			return
		}
	}
}

func tunnelResolveIP(cfg *vm.VMConfig, state *vm.VMState) (string, error) {
	if cfg.NIC.MAC != "" {
		if ip, err := vm.ResolveIPFromMAC(cfg.NIC.MAC); err == nil {
			return ip, nil
		}
	}
	if state.QGASocket != "" {
		return vm.ResolveIPFromQGA(state.QGASocket)
	}
	return "", fmt.Errorf("cannot resolve IP: no MAC in ARP table and no guest agent socket")
}

// runTCPProxy listens on localPort and proxies connections to vmIP:remotePort.
// Blocks until Ctrl+C.
func runTCPProxy(vmName string, localPort int, vmIP string, remotePort int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	fmt.Println("press Ctrl+C to close")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		_ = ln.Close()
	}()

	target := fmt.Sprintf("%s:%d", vmIP, remotePort)
	_ = vmName
	for {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return nil
		}
		go proxyConn(conn, target)
	}
}

func proxyConn(local net.Conn, remote string) {
	defer func() { _ = local.Close() }()
	rem, err := net.Dial("tcp", remote)
	if err != nil {
		return
	}
	defer func() { _ = rem.Close() }()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(rem, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, rem); done <- struct{}{} }()
	<-done
}

// runSSHTunnel opens an ssh -L tunnel for user-mode VMs.
func runSSHTunnel(vmName string, localPort int, sshHost string, sshPort int, remotePort int, cfg *vm.VMConfig) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}
	identity := home + "/.vee/ssh/id_ed25519"

	controlPath, err := tempTunnelControlPath()
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	defer func() { _ = os.Remove(controlPath) }()

	user := ""
	if cfg.CloudInit != nil && cfg.CloudInit.User != "" {
		user = cfg.CloudInit.User
	}
	dest := sshHost
	if user != "" {
		dest = user + "@" + sshHost
	}

	sshArgs := []string{
		"-fN", "-M", "-S", controlPath,
		"-i", identity,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-L", fmt.Sprintf("%d:localhost:%d", localPort, remotePort),
	}
	if sshPort != 22 {
		sshArgs = append(sshArgs, "-p", strconv.Itoa(sshPort))
	}
	sshArgs = append(sshArgs, dest)

	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh not found: %w", err)
	}
	tunnelSSH := exec.Command(sshBin, sshArgs...)
	tunnelSSH.Stdout = os.Stdout
	tunnelSSH.Stderr = os.Stderr
	if err := tunnelSSH.Run(); err != nil {
		return fmt.Errorf("open tunnel: %w", err)
	}

	fmt.Println("press Ctrl+C to close")
	_ = vmName

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	fmt.Println("\nclosing tunnel")
	_ = exec.Command(sshBin, "-S", controlPath, "-O", "exit", dest).Run()
	return nil
}

func tempTunnelControlPath() (string, error) {
	f, err := os.CreateTemp("", "vee-tunnel-*.sock")
	if err != nil {
		return "", err
	}
	path := f.Name()
	_ = f.Close()
	_ = os.Remove(path)
	return path, nil
}

// completeTunnelArgs provides completion for both the VM name (pos 1) and
// service name (pos 2).
func completeTunnelArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeVMNames(cmd, args, toComplete)
	}
	if len(args) == 1 {
		return completeServiceNames(args[0])
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func completeServiceNames(vmName string) ([]string, cobra.ShellCompDirective) {
	entry, err := findVM(vmName)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, s := range entry.Config.Services {
		out = append(out, s.Name+"\t"+string(s.Protocol))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
