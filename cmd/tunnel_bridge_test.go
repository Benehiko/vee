package cmd

import (
	"context"
	"net"
	"testing"

	"github.com/Benehiko/vee/internal/vm"
)

// TestTunnelSSHEndpointBridgeNoRecordedPort pins the regression behind
// "vee tunnel torrents qbittorrent" hanging on a bridged VM.
//
// Manager records state.SSHPort only for user-mode NAT guests; on a bridge it
// stays zero because there is no hostfwd to record. The fallback used to be
// gated on state.SSHPort > 0, so a bridged guest behind a VPN kill-switch —
// every LAN port dropped except 22 — fell through to the direct TCP proxy and
// hung on every connection. Such a guest is still reachable on 22 at its own
// address, which is what vee ssh uses.
func TestTunnelSSHEndpointBridgeNoRecordedPort(t *testing.T) {
	host, port := tunnelSSHEndpoint(context.Background(), "192.168.178.95", &vm.VMState{})
	if host != "192.168.178.95" || port != 22 {
		t.Fatalf("bridge VM with no recorded SSH port must tunnel over the guest IP on 22, got %s:%d", host, port)
	}
}

// TestTunnelSSHEndpointPrefersLiveLoopback checks that a recorded loopback port
// still wins when something actually answers there (QEMU hostfwd, or the
// daemon's bridge proxy).
func TestTunnelSSHEndpointPrefersLiveLoopback(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	live := ln.Addr().(*net.TCPAddr).Port

	host, port := tunnelSSHEndpoint(context.Background(), "192.168.178.95", &vm.VMState{SSHPort: live})
	if host != "127.0.0.1" || port != live {
		t.Fatalf("a live recorded loopback port must win, got %s:%d want 127.0.0.1:%d", host, port, live)
	}
}

// TestTunnelSSHEndpointDeadLoopbackFallsBack guards the probe: a state written
// by an older vee can carry a port nothing serves, and the daemon proxy goes
// away when the daemon stops. Either way the dead port must be skipped rather
// than tunnelled over.
func TestTunnelSSHEndpointDeadLoopbackFallsBack(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dead := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // nothing answers here now

	host, port := tunnelSSHEndpoint(context.Background(), "192.168.178.95", &vm.VMState{SSHPort: dead})
	if host != "192.168.178.95" || port != 22 {
		t.Fatalf("a dead recorded port must fall back to the guest IP on 22, got %s:%d", host, port)
	}
}

// TestTunnelSSHEndpointNoIPNoPort covers the case with nothing to go on: the
// caller must report a clear error instead of opening a proxy that hangs.
func TestTunnelSSHEndpointNoIPNoPort(t *testing.T) {
	if host, port := tunnelSSHEndpoint(context.Background(), "", &vm.VMState{}); port != 0 || host != "" {
		t.Fatalf("no IP and no recorded port must yield no endpoint, got %s:%d", host, port)
	}
}
