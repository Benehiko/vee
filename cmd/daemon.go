package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/qemubin"
	"github.com/Benehiko/vee/internal/vm"
)

// The daemon lifecycle is platform-specific: Linux installs a systemd
// system unit (daemon_linux.go), macOS a launchd LaunchDaemon
// (daemon_darwin.go), and other platforms have no installer
// (daemon_other.go). Each platform file provides daemonInstall /
// daemonUninstall plus the daemonInstallShort/Long and daemonUninstallShort
// help strings. The daemon loop itself (vm.Manager.RunDaemon) is shared.

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the vee daemon (starts and watches autostart VMs)",
	Long: `Run vee as a long-lived daemon. On startup it starts all VMs with
autostart=true, then polls periodically and restarts any that have exited.

Intended to be invoked by the service installed with:
  vee daemon install
(a systemd system unit on Linux, a launchd LaunchDaemon on macOS).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// The daemon hard-fails without a verified QEMU binary and relies on
		// the service manager's restart policy to retry Ensure until the
		// download succeeds. Softening this for QEMU-less (vz-only) setups
		// needs a lazy, per-start Ensure so a QEMU VM created later still
		// gets the pinned binary — that lands with the vz backend (issue
		// #51 V2).
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
	Short: daemonInstallShort,
	Long:  daemonInstallLong,
	RunE: func(cmd *cobra.Command, args []string) error {
		return daemonInstall(cmd.Context())
	},
}

var daemonUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: daemonUninstallShort,
	RunE: func(cmd *cobra.Command, args []string) error {
		return daemonUninstall(cmd.Context())
	},
}

// invokingUser resolves the real user vee should run as inside the system
// service. When vee daemon install is run via sudo, the original user is in
// SUDO_USER; otherwise fall back to the current user.
func invokingUser() (*user.User, error) {
	if name := os.Getenv("SUDO_USER"); name != "" {
		return user.Lookup(name)
	}
	return user.Current()
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
	//nolint:gosec,noctx // sudo/pkexec from LookPath; writes fixed vee-owned config paths; cmd is run later by caller.
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

// stringWriter is a tiny io.Writer that buffers into a string. Avoids
// pulling in bytes.Buffer just to render a small template.
type stringWriter struct{ b []byte }

func (s *stringWriter) Write(p []byte) (int, error) {
	s.b = append(s.b, p...)
	return len(p), nil
}

func (s *stringWriter) String() string { return string(s.b) }

func init() {
	daemonCmd.AddCommand(daemonInstallCmd)
	daemonCmd.AddCommand(daemonUninstallCmd)
}
