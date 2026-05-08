package cmd

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/Benehiko/vee/vm"
	"github.com/spf13/cobra"
)

var tunnelCmd = &cobra.Command{
	Use:   "tunnel <name> <port>",
	Short: "Forward a VM port to a random local port",
	Long: `Forward a port from a running VM to localhost.

For bridge-mode VMs (e.g. TrueNAS), opens a direct TCP proxy — no SSH needed.
For user-mode VMs, opens an SSH -L tunnel using the vee keypair.

The local port is chosen randomly and printed on startup.`,
	Args:              cobra.ExactArgs(2),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		remotePort, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("invalid port %q: %w", args[1], err)
		}

		cfg, state, err := loadRunningVM(name)
		if err != nil {
			return err
		}

		localPort, err := freeLocalPort()
		if err != nil {
			return fmt.Errorf("find free local port: %w", err)
		}

		switch {
		case cfg.NIC.Mode == "bridge" || (cfg.NIC.Mode == "" && state.SSHPort == 0):
			vmIP, resolveErr := tunnelResolveIP(cfg, state)
			if resolveErr != nil {
				return resolveErr
			}
			return runTCPProxy(name, localPort, vmIP, remotePort)

		case state.SSHPort > 0:
			return runSSHTunnel(name, localPort, "127.0.0.1", state.SSHPort, remotePort, cfg)

		default:
			return fmt.Errorf("VM %q: cannot determine tunnel method (not bridge, no SSH port)", name)
		}
	},
}

func tunnelResolveIP(cfg *vm.VMConfig, state *vm.VMState) (string, error) {
	if cfg.NIC.MAC != "" {
		if ip, err := resolveIPFromMAC(cfg.NIC.MAC); err == nil {
			return ip, nil
		}
	}
	if state.QGASocket != "" {
		return resolveIPFromQGA(state.QGASocket)
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

	fmt.Printf("tunnelling localhost:%d → %s:%d\n", localPort, vmName, remotePort)
	fmt.Println("press Ctrl+C to close")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		_ = ln.Close()
	}()

	target := fmt.Sprintf("%s:%d", vmIP, remotePort)
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

	fmt.Printf("tunnelling localhost:%d → %s:%d\n", localPort, vmName, remotePort)
	fmt.Println("press Ctrl+C to close")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	fmt.Println("\nclosing tunnel")
	_ = exec.Command(sshBin, "-S", controlPath, "-O", "exit", dest).Run()
	return nil
}

func freeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
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
