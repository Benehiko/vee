//go:build windows

package vm

import (
	"fmt"
	"net"
)

// controlSocketAddr returns the address vee uses to reach a QEMU control
// channel (QMP or the guest agent). Windows cannot serve these over an AF_UNIX
// socket file the way unix does, so vee binds them to an ephemeral loopback TCP
// port and returns "127.0.0.1:<port>". The port is chosen by asking the kernel
// for a free one and immediately releasing it; QEMU then binds it. There is a
// small window between release and QEMU's bind, but the loopback ephemeral
// range makes a collision unlikely for local single-user use. The resulting
// address is persisted in VMState so later commands (stop, status) reconnect to
// the same endpoint.
//
// vmDir and base are accepted for signature parity with the unix build; only
// base is used, to keep log/error messages meaningful.
func controlSocketAddr(_ /*vmDir*/, base string) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("allocate loopback port for %s control channel: %w", base, err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	port := addr.Port
	if cerr := ln.Close(); cerr != nil {
		return "", fmt.Errorf("release loopback port for %s control channel: %w", base, cerr)
	}
	return fmt.Sprintf("127.0.0.1:%d", port), nil
}
