package cmd

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/vm"
)

var tunnelSerialRaw bool

// ansiEscape matches ANSI/VT100 escape sequences and lone CR used for cursor
// control by bootloaders and terminal UIs (e.g. GRUB, UEFI shell). Covers:
// CSI sequences (\x1b[...X), two-char sequences (\x1bX), and bare \r.
var ansiEscape = regexp.MustCompile(`\x1b(?:\[[0-9;?=]*[ -/]*[@-~]|[=><~]|[@-Z\\-_])|\r`)

var tunnelCmd = &cobra.Command{
	Use:   "tunnel <name> [service]",
	Short: "Connect to a VM service (opens browser for HTTP/S, prints URL for others)",
	Long: `Show and connect to services exposed by a running VM.

Without a service argument, lists all available services and their connection URLs.
With a service argument, immediately opens or connects to that service.

HTTP/HTTPS services open in the default browser.
SPICE services print a spice:// URL.
TCP services print the forwarded address.

For bridge-mode VMs, opens a direct TCP proxy — no SSH needed. If the guest
firewall blocks the port from the LAN (e.g. a VPN kill-switch), falls back to
an SSH -L tunnel.
For user-mode VMs, opens an SSH -L tunnel.`,
	Args:              cobra.RangeArgs(1, 2),
	ValidArgsFunction: completeTunnelArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		// serial is a built-in pseudo-service backed by a local log file.
		// It works regardless of whether the VM is running.
		if len(args) == 2 && args[1] == "serial" {
			return tunnelSerial(cmd, name)
		}

		cfg, state, err := loadRunningVM(name)
		if err != nil {
			return err
		}

		services := resolvedServices(cfg, state)

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

// resolvedService aliases the shared resolution type in internal/vm, which
// the MCP server's vm_services uses too — one resolution path, no drift.
type resolvedService = vm.ResolvedService

func resolvedServices(cfg *vm.VMConfig, state *vm.VMState) []resolvedService {
	return vm.ResolvedServices(cfg, state)
}

func printServiceMenu(cfg *vm.VMConfig, services []resolvedService) error {
	fmt.Printf("%-16s  %-10s  %s\n", "SERVICE", "PROTOCOL", "CONNECTION")
	fmt.Println(strings.Repeat("─", 60))
	fmt.Printf("%-16s  %-10s  %s\n", "serial", "file", "serial console log (follow)")
	for _, s := range services {
		fmt.Printf("%-16s  %-10s  %s\n", s.Name, s.Protocol, serviceURL(cfg, s))
	}
	fmt.Println()
	fmt.Println("Run: vee tunnel <vm> <service>  to connect")
	return nil
}

// tunnelSerial streams the VM's serial console log (serial.log) to stdout,
// following new output until Ctrl+C. Works whether the VM is running or not.
// ANSI escape codes are stripped by default; pass --raw to disable.
func tunnelSerial(cmd *cobra.Command, name string) error {
	logPath := filepath.Join(prov.Config().StoragePath, name, "serial.log")
	f, err := os.Open(logPath) //nolint:gosec // logPath is derived from vee-managed storage path and VM name.
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no serial log for VM %q (has it been started?)", name)
		}
		return err
	}
	defer func() { _ = f.Close() }()

	dst := serialWriter()
	if _, err := io.Copy(dst, f); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "--- following serial output (Ctrl+C to stop) ---")
	for {
		select {
		case <-cmd.Context().Done():
			return nil
		case <-time.After(250 * time.Millisecond):
			if _, err := io.Copy(dst, f); err != nil {
				return err
			}
		}
	}
}

func init() {
	tunnelCmd.Flags().BoolVar(&tunnelSerialRaw, "raw", false, "Stream serial output without stripping ANSI escape codes")
}

// serialWriter returns a writer that strips ANSI escape codes unless --raw.
func serialWriter() io.Writer {
	if tunnelSerialRaw {
		return os.Stdout
	}
	return &ansiStripper{w: os.Stdout}
}

// ansiStripper wraps a writer and removes ANSI/VT100 escape sequences.
type ansiStripper struct {
	w io.Writer
}

func (s *ansiStripper) Write(p []byte) (int, error) {
	clean := ansiEscape.ReplaceAll(p, nil)
	if _, err := s.w.Write(clean); err != nil {
		return 0, err
	}
	return len(p), nil
}

func serviceURL(cfg *vm.VMConfig, s resolvedService) string {
	return vm.ServiceURL(cfg, s)
}

func connectService(cmd *cobra.Command, cfg *vm.VMConfig, state *vm.VMState, s resolvedService) error {
	localPort, err := freeLocalPort()
	if err != nil {
		return fmt.Errorf("find free local port: %w", err)
	}

	// SPICE: for bridge-mode VMs the SPICE port is already bound on the host
	// (QEMU binds it). No tunnel needed — launch a client directly.
	if s.Protocol == vm.ServiceSPICE {
		port := s.Port
		if port == 0 {
			return fmt.Errorf("SPICE port not yet assigned (has the VM started?)")
		}
		url := fmt.Sprintf("spice://localhost:%d", port)
		if launchSPICEClient(url) {
			return nil
		}
		fmt.Println(url)
		fmt.Println("No SPICE client found. Install one of: remote-viewer, spicy, remmina")
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
		vmIP, resolveErr := tunnelResolveIP(cmd.Context(), cfg, state)
		if resolveErr != nil {
			return resolveErr
		}
		// Kill-switched guests (e.g. the torrent template) accept only SSH
		// from the LAN, so a direct proxy would hang on every connection.
		if !tcpReachable(cmd.Context(), vmIP, s.Port) && state.SSHPort > 0 {
			fmt.Printf("%s:%d not reachable directly (guest firewall?) — tunnelling over SSH\n", vmIP, s.Port)
			url := localServiceURL(s, localPort)
			fmt.Printf("tunnelling localhost:%d → %s:%d\n", localPort, cfg.Name, s.Port)
			fmt.Println(url)
			maybeBrowser(s, url)
			return runSSHTunnel(cfg.Name, localPort, "127.0.0.1", state.SSHPort, s.Port, cfg)
		}
		url := localServiceURL(s, localPort)
		fmt.Printf("tunnelling localhost:%d → %s:%d\n", localPort, cfg.Name, s.Port)
		fmt.Println(url)
		maybeBrowser(s, url)
		return runTCPProxy(cmd.Context(), cfg.Name, localPort, vmIP, s.Port)

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

func findHostFwd(fwds []string, guestPort int) int {
	return vm.FindHostFwd(fwds, guestPort)
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
		if launchSPICEClient(url) {
			return nil
		}
		fmt.Println(url)
		fmt.Println("No SPICE client found. Install one of: remote-viewer, spicy, remmina")
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
			//nolint:gosec,noctx // path from LookPath; url is vee-constructed; fire-and-forget browser launch, no ctx.
			_ = exec.Command(path, url).Start()
			return
		}
	}
}

// launchSPICEClient tries known SPICE clients in order of preference and
// launches the first one found. Returns true if a client was launched.
func launchSPICEClient(url string) bool {
	candidates := []struct {
		bin  string
		args func(string) []string
	}{
		{"remote-viewer", func(u string) []string { return []string{u} }},
		{"spicy", func(u string) []string {
			// spicy takes --host and --port separately
			// parse host:port from spice://localhost:PORT
			u = strings.TrimPrefix(u, "spice://")
			host, port, _ := strings.Cut(u, ":")
			return []string{"--host", host, "--port", port}
		}},
		{"remmina", func(u string) []string { return []string{u} }},
	}
	for _, c := range candidates {
		if path, err := exec.LookPath(c.bin); err == nil {
			fmt.Printf("launching %s\n", c.bin)
			//nolint:gosec,noctx // path from LookPath; args from vee-constructed spice:// URL; fire-and-forget launch, no ctx.
			_ = exec.Command(path, c.args(url)...).Start()
			return true
		}
	}
	return false
}

// tcpReachable reports whether host:port accepts a TCP connection within a
// short timeout. A dropped SYN (firewalled guest) times out rather than
// erroring, hence the tight deadline.
func tcpReachable(ctx context.Context, host string, port int) bool {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func tunnelResolveIP(ctx context.Context, cfg *vm.VMConfig, state *vm.VMState) (string, error) {
	if cfg.NIC.MAC != "" {
		if ip, err := vm.ResolveIPFromMAC(cfg.NIC.MAC); err == nil {
			return ip, nil
		}
	}
	if state.QGASocket != "" {
		return vm.ResolveIPFromQGA(ctx, state.QGASocket)
	}
	return "", fmt.Errorf("cannot resolve IP: no MAC in ARP table and no guest agent socket")
}

// runTCPProxy listens on localPort and proxies connections to vmIP:remotePort.
// Blocks until Ctrl+C.
func runTCPProxy(ctx context.Context, vmName string, localPort int, vmIP string, remotePort int) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	fmt.Println("press Ctrl+C to close")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, stopSignals()...)
	go func() {
		<-quit
		_ = ln.Close()
	}()

	target := fmt.Sprintf("%s:%d", vmIP, remotePort)
	_ = vmName
	for {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			//nolint:nilerr // Accept fails when the listener is closed on Ctrl+C; treat as clean shutdown.
			return nil
		}
		go proxyConn(ctx, conn, target)
	}
}

func proxyConn(ctx context.Context, local net.Conn, remote string) {
	defer func() { _ = local.Close() }()
	var d net.Dialer
	rem, err := d.DialContext(ctx, "tcp", remote)
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
func runSSHTunnel(vmName string, localPort int, sshHost string, sshPort, remotePort int, cfg *vm.VMConfig) error {
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

	user := tunnelSSHUser(cfg)
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
	//nolint:gosec,noctx // sshBin from LookPath; args from vetted VM config; ssh -fN backgrounds itself, no ctx here.
	tunnelSSH := exec.Command(sshBin, sshArgs...)
	tunnelSSH.Stdout = os.Stdout
	tunnelSSH.Stderr = os.Stderr
	if err := tunnelSSH.Run(); err != nil {
		return fmt.Errorf("open tunnel: %w", err)
	}

	fmt.Println("press Ctrl+C to close")
	_ = vmName

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, stopSignals()...)
	<-quit
	fmt.Println("\nclosing tunnel")
	//nolint:gosec,noctx // sshBin from LookPath; closes the previously-opened SSH control socket, no ctx here.
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
	out := []string{"serial\tserial console log"}
	entry, err := findVM(vmName)
	if err != nil {
		return out, cobra.ShellCompDirectiveNoFileComp
	}
	for _, s := range entry.Config.Services {
		out = append(out, s.Name+"\t"+string(s.Protocol))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// tunnelSSHUser resolves the account the tunnel logs in as, matching vee ssh.
//
// Reading cfg.CloudInit.User directly is not enough, and getting this wrong is
// not a visible error: it misses the explicit ssh_user override, and it misses
// templates that create no user of their own and inject the SSH keys into the
// image's default account instead (docker, dns-sink, bitmagnet — the Alpine
// image ships no bash for useradd). For those CloudInit.User is empty, so
// ssh(1) substitutes the host username and the tunnel fails with "Permission
// denied (publickey)" against an account that never existed in the guest.
//
// Returns "" when the config carries no account at all (e.g. macOS guests), in
// which case ssh's own default applies.
func tunnelSSHUser(cfg *vm.VMConfig) string {
	if user := cfg.SSHUsername(); user != "" {
		return user
	}
	// TrueNAS stores its admin account separately from cloud-init.
	if cfg.Template == "truenas" {
		return cfg.TrueNASUser
	}
	return ""
}
