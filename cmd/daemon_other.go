//go:build !linux && !darwin

package cmd

import (
	"context"
	"fmt"
	"runtime"
)

const (
	daemonInstallShort = "Install the vee background service (Linux and macOS only)"
	daemonInstallLong  = `Installing the vee daemon as a system service is supported on Linux
(systemd) and macOS (launchd) only. On this platform, run "vee daemon"
manually if you need autostart supervision.`
	daemonUninstallShort = "Remove the vee background service (Linux and macOS only)"
)

func daemonInstall(context.Context) error {
	return fmt.Errorf("vee daemon install is not supported on %s (Linux systemd and macOS launchd only)", runtime.GOOS)
}

func daemonUninstall(context.Context) error {
	return fmt.Errorf("vee daemon uninstall is not supported on %s (Linux systemd and macOS launchd only)", runtime.GOOS)
}
