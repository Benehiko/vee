package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"

	"github.com/Benehiko/vee/internal/qemubin"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the vee daemon (starts and watches autostart VMs)",
	Long: `Run vee as a long-lived daemon. On startup it starts all VMs with
autostart=true, then polls every 30s and restarts any that have exited.

Intended to be invoked by the systemd user service installed with:
  vee daemon install`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if qemuPath, err := qemubin.Ensure(); err != nil {
			return fmt.Errorf("qemu binary: %w", err)
		} else {
			prov.Config().QemuBinaryPath = qemuPath
		}
		mgr := vm.NewManager(prov)
		return mgr.RunDaemon(cmd.Context())
	},
}

var daemonInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install and enable the vee systemd user service",
	RunE: func(cmd *cobra.Command, args []string) error {
		return installSystemdUnit()
	},
}

var daemonUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Disable and remove the vee systemd user service",
	RunE: func(cmd *cobra.Command, args []string) error {
		return uninstallSystemdUnit()
	},
}

const unitTemplate = `[Unit]
Description=vee VM daemon
Documentation=https://github.com/Benehiko/vee
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{.VeeBin}} daemon
Restart=on-failure
RestartSec=5s
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
`

func unitDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

func unitPath() (string, error) {
	dir, err := unitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "vee.service"), nil
}

func installSystemdUnit() error {
	veeBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve vee binary: %w", err)
	}

	dir, err := unitDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	path, err := unitPath()
	if err != nil {
		return err
	}

	tmpl, err := template.New("unit").Parse(unitTemplate)
	if err != nil {
		return err
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := tmpl.Execute(f, struct{ VeeBin string }{veeBin}); err != nil {
		return err
	}

	// daemon-reload + enable + start
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "--now", "vee.service"},
	} {
		out, cmdErr := exec.Command("systemctl", args...).CombinedOutput()
		if cmdErr != nil {
			return fmt.Errorf("systemctl %v: %w\n%s", args, cmdErr, out)
		}
	}

	fmt.Printf("vee.service installed at %s\n", path)
	fmt.Println("Service enabled and started. Check status with: systemctl --user status vee")
	return nil
}

func uninstallSystemdUnit() error {
	for _, args := range [][]string{
		{"--user", "disable", "--now", "vee.service"},
	} {
		out, err := exec.Command("systemctl", args...).CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "systemctl %v: %v\n%s\n", args, err, out)
		}
	}

	path, err := unitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}

	out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w\n%s", err, out)
	}

	fmt.Println("vee.service removed.")
	return nil
}

func init() {
	daemonCmd.AddCommand(daemonInstallCmd)
	daemonCmd.AddCommand(daemonUninstallCmd)
}
