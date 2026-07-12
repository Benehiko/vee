package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"text/template"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/qemubin"
	"github.com/Benehiko/vee/internal/vm"
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
	Short: "Install and enable the vee systemd system service",
	Long: `Install /etc/systemd/system/vee.service as a system-level unit
running as your user account. A system unit (rather than a --user unit) is
required so the daemon survives session teardown during host shutdown and
can react to logind's PrepareForShutdown signal in time to stop VMs
gracefully.

Requires root (sudo).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := installVFIOModprobeConf(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write vfio modprobe config: %v\n", err)
		}
		if err := uninstallLegacyUserUnit(cmd.Context()); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove legacy --user vee.service: %v\n", err)
		}
		return installSystemdUnit()
	},
}

var daemonUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Disable and remove the vee systemd system service",
	RunE: func(cmd *cobra.Command, args []string) error {
		return uninstallSystemdUnit(cmd.Context())
	},
}

// unitTemplate is the system-level vee.service. It runs as the invoking user
// (User= / Group=) so file paths under $HOME, the user bus, and any qemu /
// helper binaries continue to work. Ordering and TimeoutStopSec together
// give the daemon enough time to react to PrepareForShutdown and gracefully
// power off every VM before systemd hard-kills the cgroup.
const unitTemplate = `[Unit]
Description=vee VM daemon
Documentation=https://github.com/Benehiko/vee
After=network-online.target multi-user.target
Wants=network-online.target
# Order this unit before shutdown/reboot/halt so we receive SIGTERM and
# react to logind's PrepareForShutdown signal before the host actually
# powers off.
Before=shutdown.target reboot.target halt.target
Conflicts=shutdown.target

[Service]
Type=simple
User={{.User}}
Group={{.Group}}
Environment=HOME={{.Home}}
Environment=XDG_RUNTIME_DIR=/run/user/{{.UID}}
Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/{{.UID}}/bus
ExecStart={{.VeeBin}} daemon
Restart=on-failure
RestartSec=5s
# Allow up to 5 minutes for graceful VM shutdown on host poweroff. Each VM
# stop is bounded by the daemon's own per-VM timeout.
TimeoutStopSec=300s
KillSignal=SIGTERM
# Send SIGTERM only to the main process; we don't want the cgroup-wide kill
# to take down qemu children before the daemon has issued a graceful QMP
# system_powerdown.
KillMode=mixed
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`

const vfioModprobeConf = `# Written by vee daemon install
# Prevents vfio-pci from runtime-suspending bound devices to D3cold.
# Without this, QEMU aborts with pci_irq_handler assertion failure when it
# opens a device that suspended between vfio-pci bind and QEMU attach.
options vfio-pci disable_idle_d3=1
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
			//nolint:gosec,noctx // initramfs tool path from LookPath; fixed args; best-effort install step, no ctx here.
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

const systemUnitPath = "/etc/systemd/system/vee.service"

const polkitRulePath = "/etc/polkit-1/rules.d/49-vee-inhibit.rules"

const udevRulePath = "/etc/udev/rules.d/99-vee-vfio.rules"

// udevRuleTemplate grants the invoking user group rw access to the sysfs
// power/control and reset nodes for every device bound to vfio-pci. This
// lets vee wake a GPU from D3cold (power/control) and trigger a function-
// level reset (reset) without requiring sudo on each vee start.
//
// The rule matches the device itself (ACTION=="add|change", DRIVER=="vfio-pci")
// and its power subdirectory attribute. Because the power/* attrs live in a
// sub-directory they need a separate RUN+="chmod" line — udev ATTR{} can
// only set the device node itself, not child directories.
const udevRuleTemplate = `# Generated by vee daemon install.
# 1. Disables runtime autosuspend for vfio-pci devices so the GPU never
#    enters D3cold while bound to vfio-pci. This is the reliable alternative
#    to the vfio-pci enable_runtime_pm=0 module parameter (which not all
#    kernel builds expose).
# 2. Grants {{.Group}} rw access to power/control and reset so vee can wake
#    a GPU that ended up in D3cold (e.g. after a QEMU crash) without sudo.
ACTION=="add|change", DRIVER=="vfio-pci", \
  ATTR{power/control}="on", \
  ATTR{power/autosuspend_delay_ms}="-1", \
  OWNER="root", GROUP="{{.Group}}", \
  RUN+="/bin/chgrp {{.Group}} /sys%p/reset", \
  RUN+="/bin/chmod g+w /sys%p/reset", \
  RUN+="/bin/chgrp {{.Group}} /sys%p/power/control", \
  RUN+="/bin/chmod g+w /sys%p/power/control"
`

// polkitRuleTemplate grants the vee daemon user permission to take a
// "block" inhibitor for shutdown. logind requires polkit authorization for
// this action, and a system service running as a regular user has no
// graphical session for polkit to use as evidence of presence — so the
// inhibit call fails with "interactive authentication required". This rule
// pre-authorizes the specific user the unit runs as, without granting any
// broader privilege.
const polkitRuleTemplate = `// Generated by ` + "`vee daemon install`" + `.
// Allows the user that runs the vee system service to acquire a logind
// shutdown inhibitor so the host blocks on power-off until vee has
// gracefully stopped every running VM.
polkit.addRule(function(action, subject) {
    if ((action.id == "org.freedesktop.login1.inhibit-block-shutdown" ||
         action.id == "org.freedesktop.login1.inhibit-delay-shutdown") &&
        subject.user == "{{.User}}") {
        return polkit.Result.YES;
    }
});
`

// invokingUser resolves the real user vee should run as inside the system
// service. When vee daemon install is run via sudo, the original user is in
// SUDO_USER; otherwise fall back to the current user.
func invokingUser() (*user.User, error) {
	if name := os.Getenv("SUDO_USER"); name != "" {
		return user.Lookup(name)
	}
	return user.Current()
}

func installSystemdUnit() error {
	veeBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve vee binary: %w", err)
	}

	u, err := invokingUser()
	if err != nil {
		return fmt.Errorf("resolve invoking user: %w", err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		return fmt.Errorf("resolve primary group: %w", err)
	}

	tmpl, err := template.New("unit").Parse(unitTemplate)
	if err != nil {
		return err
	}

	var buf []byte
	{
		var sb stringWriter
		if err := tmpl.Execute(&sb, struct {
			VeeBin, User, Group, Home, UID string
		}{
			VeeBin: veeBin,
			User:   u.Username,
			Group:  g.Name,
			Home:   u.HomeDir,
			UID:    u.Uid,
		}); err != nil {
			return err
		}
		buf = []byte(sb.String())
	}

	writeCmd, err := sudoWriteCmd(systemUnitPath, string(buf))
	if err != nil {
		return err
	}
	if out, err := writeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write %s: %w\n%s", systemUnitPath, err, out)
	}

	if err := installPolkitRule(u.Username); err != nil {
		return fmt.Errorf("install polkit rule: %w", err)
	}

	if err := installUdevRule(u.Username); err != nil {
		return fmt.Errorf("install udev rule: %w", err)
	}

	for _, sargs := range [][]string{
		{"sudo", "systemctl", "daemon-reload"},
		{"sudo", "systemctl", "enable", "--now", "vee.service"},
	} {
		//nolint:gosec,noctx // fixed systemctl argument list; install step, no ctx in this call chain.
		out, cmdErr := exec.Command(sargs[0], sargs[1:]...).CombinedOutput()
		if cmdErr != nil {
			prov.Logger().Debug("systemctl invocation failed",
				zap.Strings("args", sargs), zap.ByteString("output", out))
			return fmt.Errorf("vee daemon install failed during %v: %w", sargs, cmdErr)
		}
	}

	fmt.Printf("vee.service installed at %s\n", systemUnitPath)
	fmt.Println("Service enabled and started. Check status with: systemctl status vee")
	return nil
}

func uninstallSystemdUnit(ctx context.Context) error {
	for _, sargs := range [][]string{
		{"sudo", "systemctl", "disable", "--now", "vee.service"},
	} {
		//nolint:gosec // fixed systemctl argument list; not tainted user input.
		out, err := exec.CommandContext(ctx, sargs[0], sargs[1:]...).CombinedOutput()
		if err != nil {
			prov.Logger().Debug("systemctl invocation failed during uninstall",
				zap.Strings("args", sargs), zap.ByteString("output", out), zap.Error(err))
			fmt.Fprintf(os.Stderr, "warning: vee daemon uninstall step %v did not complete cleanly\n", sargs)
		}
	}

	if out, err := exec.CommandContext(ctx, "sudo", "rm", "-f", systemUnitPath, polkitRulePath, udevRulePath).CombinedOutput(); err != nil {
		return fmt.Errorf("remove unit/polkit/udev files: %w\n%s", err, out)
	}
	// Reload udev so the removed rule stops applying to future device events.
	_, _ = exec.CommandContext(ctx, "sudo", "udevadm", "control", "--reload-rules").CombinedOutput()

	out, err := exec.CommandContext(ctx, "sudo", "systemctl", "daemon-reload").CombinedOutput()
	if err != nil {
		prov.Logger().Debug("systemctl daemon-reload failed",
			zap.ByteString("output", out), zap.Error(err))
		return fmt.Errorf("vee daemon uninstall failed during reload: %w", err)
	}

	fmt.Println("vee.service removed.")
	return nil
}

// installUdevRule writes the udev rule that grants the user's primary group
// write access to vfio-pci device sysfs power/control and reset nodes.
func installUdevRule(username string) error {
	u, err := user.Lookup(username)
	if err != nil {
		return fmt.Errorf("lookup user %s: %w", username, err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		return fmt.Errorf("lookup group for %s: %w", username, err)
	}

	tmpl, err := template.New("udev").Parse(udevRuleTemplate)
	if err != nil {
		return err
	}
	var sb stringWriter
	if err := tmpl.Execute(&sb, struct{ Group string }{Group: g.Name}); err != nil {
		return err
	}

	writeCmd, err := sudoWriteCmd(udevRulePath, sb.String())
	if err != nil {
		return err
	}
	if out, err := writeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write %s: %w\n%s", udevRulePath, err, out)
	}
	fmt.Printf("udev rule installed at %s (group=%s)\n", udevRulePath, g.Name)

	// Reload udev rules and trigger for already-bound vfio-pci devices.
	for _, args := range [][]string{
		{"sudo", "udevadm", "control", "--reload-rules"},
		{"sudo", "udevadm", "trigger", "--subsystem-match=pci", "--attr-match=driver=vfio-pci", "--action=change"},
	} {
		//nolint:gosec,noctx // fixed udevadm argument list; install step, no ctx in this call chain.
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v: %v\n%s\n", args, err, out)
		}
	}
	return nil
}

// installPolkitRule writes the polkit rule that lets the vee system
// service take a logind shutdown inhibitor. Without this rule logind
// rejects Inhibit() with "interactive authentication required" because
// the service has no graphical session.
func installPolkitRule(username string) error {
	tmpl, err := template.New("polkit").Parse(polkitRuleTemplate)
	if err != nil {
		return err
	}
	var sb stringWriter
	if err := tmpl.Execute(&sb, struct{ User string }{User: username}); err != nil {
		return err
	}

	writeCmd, err := sudoWriteCmd(polkitRulePath, sb.String())
	if err != nil {
		return err
	}
	if out, err := writeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write %s: %w\n%s", polkitRulePath, err, out)
	}
	fmt.Printf("polkit rule installed at %s (user=%s)\n", polkitRulePath, username)
	return nil
}

// uninstallLegacyUserUnit removes a previously-installed --user vee.service.
// The legacy user-scoped unit is incompatible with the system unit because
// systemd would run two vee daemons simultaneously (one per scope), and the
// user-scoped daemon does not survive session teardown during host shutdown.
func uninstallLegacyUserUnit(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".config", "systemd", "user", "vee.service")
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return nil
	}
	for _, sargs := range [][]string{
		{"--user", "disable", "--now", "vee.service"},
	} {
		//nolint:gosec // fixed systemctl argument list; not tainted user input.
		out, err := exec.CommandContext(ctx, "systemctl", sargs...).CombinedOutput()
		if err != nil {
			prov.Logger().Debug("systemctl --user disable failed",
				zap.Strings("args", sargs), zap.ByteString("output", out), zap.Error(err))
		}
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	_, _ = exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").CombinedOutput()
	fmt.Printf("Removed legacy --user vee.service at %s\n", path)
	return nil
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
