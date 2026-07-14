//go:build !windows

package qemu

import (
	"fmt"
	"os"
)

// This file defines how vee reaches QEMU's control channels (QMP and the guest
// agent) on unix hosts: AF_UNIX sockets addressed by filesystem path. The
// Windows build (control_windows.go) uses loopback TCP instead, because QEMU on
// Windows cannot serve these over a unix-socket file the same way.

// controlNetwork is the net package network name used to dial QMP/QGA.
func controlNetwork() string { return "unix" }

// qmpArg returns the value for QEMU's -qmp flag for the given control address
// (here, a unix socket path).
func qmpArg(addr string) string {
	return fmt.Sprintf("unix:%s,server,nowait", addr)
}

// qgaChardevArg returns the -chardev value backing the guest-agent virtserial
// port for the given control address (here, a unix socket path).
func qgaChardevArg(addr string) string {
	return fmt.Sprintf("socket,path=%s,server=on,wait=off,id=qga0", addr)
}

// controlReady reports whether QEMU has created the control endpoint yet. On
// unix the endpoint is a socket file, so its presence on disk is the signal.
func controlReady(addr string) bool {
	_, err := os.Stat(addr)
	return err == nil
}
