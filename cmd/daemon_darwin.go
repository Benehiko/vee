//go:build darwin

package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"text/template"
)

const (
	daemonInstallShort = "Install and start the vee launchd daemon"
	daemonInstallLong  = `Install ` + launchdPlistPath + ` as a system-level
launchd daemon running as your user account. A LaunchDaemon (rather than a
per-user LaunchAgent) is required so the daemon survives logout and receives
launchd's shutdown SIGTERM early enough to stop VMs gracefully before the
host powers off.

Uses sudo to write the plist and load the job.`
	daemonUninstallShort = "Stop and remove the vee launchd daemon"
)

// launchdLabel follows the io.vee.* convention already used for the
// macOS-guest first-boot job (io.vee.firstboot).
const launchdLabel = "io.vee.daemon"

const launchdPlistPath = "/Library/LaunchDaemons/" + launchdLabel + ".plist"

// launchdPlistTemplate is the macOS counterpart of the systemd unit in
// daemon_linux.go:
//
//   - UserName/GroupName ≈ User=/Group= — the job runs as the invoking
//     user so ~/.vee, the vz helper, and qemu all keep working.
//   - KeepAlive.SuccessfulExit=false ≈ Restart=on-failure.
//   - ExitTimeOut ≈ TimeoutStopSec — at host shutdown launchd sends
//     SIGTERM and grants the job this many seconds before SIGKILL; the
//     daemon uses the window to power off every VM (the whole batch is
//     bounded at ~90s, so 300 leaves headroom).
//   - AbandonProcessGroup ≈ KillMode=mixed — never let launchd take the
//     VM processes down with the daemon; they are stopped gracefully or
//     deliberately left running.
//
// There is no logind on macOS: the daemon maps launchd's SIGTERM to the
// same graceful-stop path (see internal/shutdown/inhibitor_darwin.go).
const launchdPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.VeeBin}}</string>
		<string>daemon</string>
	</array>
	<key>UserName</key>
	<string>{{.User}}</string>
	<key>GroupName</key>
	<string>{{.Group}}</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>HOME</key>
		<string>{{.Home}}</string>
		<key>PATH</key>
		<string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>5</integer>
	<key>ExitTimeOut</key>
	<integer>300</integer>
	<key>AbandonProcessGroup</key>
	<true/>
	<key>StandardOutPath</key>
	<string>{{.LogPath}}</string>
	<key>StandardErrorPath</key>
	<string>{{.LogPath}}</string>
</dict>
</plist>
`

// launchdPlist renders the LaunchDaemon plist for the given binary and user.
func launchdPlist(veeBin, username, group, home string) (string, error) {
	tmpl, err := template.New("plist").Parse(launchdPlistTemplate)
	if err != nil {
		return "", err
	}
	var sb stringWriter
	if err := tmpl.Execute(&sb, struct {
		VeeBin, User, Group, Home, LogPath string
	}{
		VeeBin:  veeBin,
		User:    username,
		Group:   group,
		Home:    home,
		LogPath: filepath.Join(home, ".vee", "logs", "daemon.log"),
	}); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// daemonInstall is the macOS service installer: a launchd LaunchDaemon.
// The Linux-only vfio/udev/polkit steps have no macOS equivalent and are
// skipped entirely.
func daemonInstall(ctx context.Context) error {
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

	// launchd refuses to spawn the job when StandardOutPath's directory is
	// missing, so make sure the log dir exists before loading.
	if err := ensureUserDir(u, filepath.Join(u.HomeDir, ".vee", "logs")); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not create log directory: %v\n", err)
	}

	rendered, err := launchdPlist(veeBin, u.Username, g.Name, u.HomeDir)
	if err != nil {
		return err
	}

	// Idempotence: a byte-identical plist that is already loaded needs no
	// bootout/bootstrap cycle (which would bounce the daemon and its VMs).
	if existing, rerr := os.ReadFile(launchdPlistPath); rerr == nil &&
		string(existing) == rendered && launchdJobLoaded(ctx) {
		fmt.Println("vee launchd daemon already installed and loaded:", launchdPlistPath)
		fmt.Println("Restart it with: sudo launchctl kickstart -k system/" + launchdLabel)
		return nil
	}

	writeCmd, err := sudoWriteCmd(launchdPlistPath, rendered)
	if err != nil {
		return err
	}
	if out, err := writeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write %s: %w\n%s", launchdPlistPath, err, out)
	}

	// Reload: boot out any loaded copy first (bootstrap refuses to replace
	// a loaded job). The bootout SIGTERMs a running daemon, which stops its
	// VMs gracefully; the fresh daemon's autostart pass brings them back.
	if launchdJobLoaded(ctx) {
		if out, err := launchctl(ctx, "bootout", "system/"+launchdLabel); err != nil {
			fmt.Fprintf(os.Stderr, "warning: launchctl bootout did not complete cleanly: %v\n%s", err, out)
		}
	}
	for _, args := range [][]string{
		{"bootstrap", "system", launchdPlistPath},
		{"enable", "system/" + launchdLabel},
	} {
		if out, err := launchctl(ctx, args...); err != nil {
			return fmt.Errorf("vee daemon install failed during launchctl %v: %w\n%s", args, err, out)
		}
	}

	fmt.Printf("vee launchd daemon installed at %s\n", launchdPlistPath)
	fmt.Println("Job loaded and started. Check status with: sudo launchctl print system/" + launchdLabel)
	return nil
}

// daemonUninstall boots the job out of launchd and removes the plist. The
// bootout SIGTERMs the running daemon, which gracefully stops running VMs
// before exiting (launchd grants it ExitTimeOut seconds).
func daemonUninstall(ctx context.Context) error {
	if launchdJobLoaded(ctx) {
		if out, err := launchctl(ctx, "bootout", "system/"+launchdLabel); err != nil {
			fmt.Fprintf(os.Stderr, "warning: launchctl bootout did not complete cleanly: %v\n%s", err, out)
		}
	}
	if out, err := exec.CommandContext(ctx, "sudo", "rm", "-f", launchdPlistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("remove %s: %w\n%s", launchdPlistPath, err, out)
	}
	fmt.Println("vee launchd daemon removed.")
	return nil
}

// launchctl runs `sudo launchctl <args...>` and returns combined output.
func launchctl(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"launchctl"}, args...)
	//nolint:gosec // fixed launchctl argument list; not tainted user input.
	return exec.CommandContext(ctx, "sudo", full...).CombinedOutput()
}

// launchdJobLoaded reports whether the vee job exists in launchd's system
// domain.
func launchdJobLoaded(ctx context.Context) bool {
	_, err := launchctl(ctx, "print", "system/"+launchdLabel)
	return err == nil
}

// ensureUserDir creates dir (and parents) and, when install runs under
// sudo, hands ownership to the invoking user so the daemon — which runs as
// that user — can write there.
func ensureUserDir(u *user.User, dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if os.Getuid() != 0 {
		return nil
	}
	uid, uidErr := strconv.Atoi(u.Uid)
	gid, gidErr := strconv.Atoi(u.Gid)
	if uidErr == nil && gidErr == nil {
		// Only the vee-owned segments; the home directory itself is untouched.
		for _, p := range []string{filepath.Dir(dir), dir} {
			_ = os.Chown(p, uid, gid)
		}
	}
	return nil
}
