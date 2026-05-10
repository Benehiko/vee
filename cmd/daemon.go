package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"text/template"

	dbus "github.com/godbus/dbus/v5"

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
		if err := enableLinger(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not enable systemd linger: %v\n", err)
			fmt.Fprintln(os.Stderr, "  Run manually: loginctl enable-linger $USER")
		}
		if err := installVFIOModprobeConf(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write vfio modprobe config: %v\n", err)
			fmt.Fprintln(os.Stderr, "  Run manually: echo 'options vfio-pci enable_runtime_pm=0' | sudo tee /etc/modprobe.d/vee-vfio.conf")
		}
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

// enableLinger enables systemd linger for the current user so the user service
// survives logout and starts at boot. Tries loginctl first (always present on
// systemd hosts); falls back to the D-Bus org.freedesktop.login1 API directly.
func enableLinger() error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("get current user: %w", err)
	}

	// Try loginctl first — it's part of systemd and always available alongside systemctl.
	if path, err := exec.LookPath("loginctl"); err == nil {
		out, cmdErr := exec.Command(path, "enable-linger", u.Username).CombinedOutput()
		if cmdErr == nil {
			return nil
		}
		// loginctl found but failed — fall through to D-Bus.
		_ = out
	}

	// Fall back: call org.freedesktop.login1.Manager.SetUserLinger over D-Bus.
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return fmt.Errorf("parse uid: %w", err)
	}
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connect to system D-Bus: %w", err)
	}
	defer func() { _ = conn.Close() }()

	obj := conn.Object("org.freedesktop.login1", "/org/freedesktop/login1")
	call := obj.Call("org.freedesktop.login1.Manager.SetUserLinger", 0,
		uint32(uid), // uid
		true,        // enable
		true,        // interactive (polkit prompt if needed)
	)
	if call.Err != nil {
		return fmt.Errorf("SetUserLinger D-Bus call: %w", call.Err)
	}
	return nil
}

const vfioModprobeConf = `# Written by vee daemon install
# Prevents vfio-pci from runtime-suspending GPUs to D3cold.
# Without this, a QEMU crash can leave the GPU in D3cold and require a cold reboot.
options vfio-pci enable_runtime_pm=0
`

const vfioModprobePath = "/etc/modprobe.d/vee-vfio.conf"

// installVFIOModprobeConf writes the vfio-pci modprobe options file via sudo/pkexec.
// It also regenerates the initramfs so the option is picked up on next boot.
func installVFIOModprobeConf() error {
	// Check if already installed with the right content.
	if existing, err := os.ReadFile(vfioModprobePath); err == nil {
		if string(existing) == vfioModprobeConf {
			fmt.Println("vfio modprobe config already up to date:", vfioModprobePath)
			return nil
		}
	}

	// Write via sudo tee (pkexec as fallback).
	writeCmd, err := sudoWriteCmd(vfioModprobePath, vfioModprobeConf)
	if err != nil {
		return err
	}
	if out, err := writeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write %s: %w\n%s", vfioModprobePath, err, out)
	}
	fmt.Println("Written:", vfioModprobePath)

	// Regenerate initramfs so the option is baked in — best-effort.
	for _, cmd := range [][]string{
		{"mkinitcpio", "-P"},
		{"dracut", "--regenerate-all", "--force"},
	} {
		if path, lookErr := exec.LookPath(cmd[0]); lookErr == nil {
			args := append([]string{path}, cmd[1:]...)
			sudoArgs := append([]string{"sudo"}, args...)
			out, runErr := exec.Command(sudoArgs[0], sudoArgs[1:]...).CombinedOutput()
			if runErr != nil {
				fmt.Fprintf(os.Stderr, "warning: %s failed: %v\n%s\n", cmd[0], runErr, out)
			} else {
				fmt.Printf("Initramfs regenerated via %s\n", cmd[0])
			}
			break
		}
	}

	fmt.Println("vfio-pci enable_runtime_pm=0 applied — reboot to activate.")
	return nil
}

// sudoWriteCmd returns an exec.Cmd that writes content to path via sudo tee.
func sudoWriteCmd(path, content string) (*exec.Cmd, error) {
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		pkexec, pErr := exec.LookPath("pkexec")
		if pErr != nil {
			return nil, fmt.Errorf("neither sudo nor pkexec found")
		}
		sudo = pkexec
	}
	cmd := exec.Command(sudo, "tee", path)
	cmd.Stdin = os.Stdin
	// Feed the content via a pipe.
	pr, pw, pipeErr := os.Pipe()
	if pipeErr != nil {
		return nil, pipeErr
	}
	cmd.Stdin = pr
	go func() {
		_, _ = pw.WriteString(content)
		_ = pw.Close()
	}()
	return cmd, nil
}

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
	for _, sargs := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "--now", "vee.service"},
	} {
		out, cmdErr := exec.Command("systemctl", sargs...).CombinedOutput()
		if cmdErr != nil {
			return fmt.Errorf("systemctl %v: %w\n%s", sargs, cmdErr, out)
		}
	}

	fmt.Printf("vee.service installed at %s\n", path)
	fmt.Println("Service enabled and started. Check status with: systemctl --user status vee")
	return nil
}

func uninstallSystemdUnit() error {
	for _, sargs := range [][]string{
		{"--user", "disable", "--now", "vee.service"},
	} {
		out, err := exec.Command("systemctl", sargs...).CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "systemctl %v: %v\n%s\n", sargs, err, out)
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
