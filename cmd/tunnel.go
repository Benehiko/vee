package cmd

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

var tunnelCmd = &cobra.Command{
	Use:               "tunnel <name> <port>",
	Short:             "Forward a VM port to localhost via SSH tunnel",
	Args:              cobra.ExactArgs(2),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		remotePort := args[1]

		cfg, state, err := loadRunningVM(name)
		if err != nil {
			return err
		}

		// Resolve SSH target the same way vee ssh does.
		var sshHost string
		var sshPort int

		switch {
		case state.SSHPort > 0:
			sshHost = "127.0.0.1"
			sshPort = state.SSHPort
		case cfg.NIC.Mode == "bridge" || cfg.NIC.Mode == "":
			mac := cfg.NIC.MAC
			if mac == "" {
				return fmt.Errorf("VM %q has no MAC address; cannot resolve IP", name)
			}
			ip, resolveErr := resolveIPFromMAC(mac)
			if resolveErr != nil {
				return fmt.Errorf("could not resolve IP for VM %q (MAC %s): %w", name, mac, resolveErr)
			}
			sshHost = ip
			sshPort = 22
		default:
			return fmt.Errorf("VM %q has no SSH port and is not on a bridge network", name)
		}

		// Pick a random free local port.
		localPort, err := freeLocalPort()
		if err != nil {
			return fmt.Errorf("find free local port: %w", err)
		}

		// SSH identity.
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home dir: %w", err)
		}
		identity := fmt.Sprintf("%s/.vee/ssh/id_ed25519", home)

		// Control socket for clean teardown.
		controlPath, err := tempTunnelControlPath()
		if err != nil {
			return fmt.Errorf("control socket: %w", err)
		}
		defer func() { _ = os.Remove(controlPath) }()

		// Build SSH user@host string.
		user := ""
		if cfg.CloudInit != nil && cfg.CloudInit.User != "" {
			user = cfg.CloudInit.User
		}
		dest := sshHost
		if user != "" {
			dest = user + "@" + sshHost
		}

		sshArgs := []string{
			"-fN",
			"-M", "-S", controlPath,
			"-i", identity,
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-L", fmt.Sprintf("%d:localhost:%s", localPort, remotePort),
		}
		if sshPort != 22 {
			sshArgs = append(sshArgs, "-p", fmt.Sprintf("%d", sshPort))
		}
		sshArgs = append(sshArgs, dest)

		sshBin, err := exec.LookPath("ssh")
		if err != nil {
			return fmt.Errorf("ssh not found: %w", err)
		}

		// Start the tunnel (ssh -fN returns once authenticated and forked).
		tunnelSSH := exec.Command(sshBin, sshArgs...)
		tunnelSSH.Stdout = os.Stdout
		tunnelSSH.Stderr = os.Stderr
		if err := tunnelSSH.Run(); err != nil {
			return fmt.Errorf("open tunnel: %w", err)
		}

		fmt.Printf("tunnelling localhost:%d → %s:%s\n", localPort, name, remotePort)
		fmt.Println("press Ctrl+C to close")

		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit

		fmt.Println("\nclosing tunnel")
		_ = exec.Command(sshBin, "-S", controlPath, "-O", "exit", dest).Run()
		return nil
	},
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
