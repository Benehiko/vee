//go:build windows

package qemu

import (
	"fmt"
	"net"
	"time"
)

// On Windows, QEMU cannot serve QMP/the guest agent over an AF_UNIX socket file
// the way it does on unix. vee therefore addresses these control channels as
// loopback TCP endpoints ("127.0.0.1:<port>"). The address string carried in
// qmpSocket/qgaSocket fields is a host:port on this platform, not a filesystem
// path.

// controlNetwork is the net package network name used to dial QMP/QGA.
func controlNetwork() string { return "tcp" }

// qmpArg returns the value for QEMU's -qmp flag for the given control address
// (a "host:port" loopback TCP endpoint).
func qmpArg(addr string) string {
	return fmt.Sprintf("tcp:%s,server=on,wait=off", addr)
}

// qgaChardevArg returns the -chardev value backing the guest-agent virtserial
// port for the given control address (a "host:port" loopback TCP endpoint).
func qgaChardevArg(addr string) string {
	return fmt.Sprintf("socket,host=%s,port=%s,server=on,wait=off,id=qga0", hostOf(addr), portOf(addr))
}

// controlReady reports whether QEMU has bound the control endpoint yet. There
// is no socket file to stat, so probe by attempting a short TCP connection.
func controlReady(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func hostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}
