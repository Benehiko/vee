package cmd

import (
	"fmt"
	"net"
	"time"

	"github.com/Benehiko/vee/internal/qemu"
	"github.com/Benehiko/vee/internal/vm"
)

// loadRunningVM looks up a VM by name and returns its config and state.
// Returns an error if the VM is not found or not running.
func loadRunningVM(name string) (*vm.VMConfig, *vm.VMState, error) {
	mgr := vm.NewManager(prov)
	entries, err := mgr.List()
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if e.Config.Name == name {
			if !e.State.Running {
				return nil, nil, fmt.Errorf("VM %q is not running", name)
			}
			return e.Config, e.State, nil
		}
	}
	return nil, nil, fmt.Errorf("VM %q not found", name)
}

// findVM looks up a VM by name regardless of running state.
func findVM(name string) (*vm.ListEntry, error) {
	mgr := vm.NewManager(prov)
	entries, err := mgr.List()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Config.Name == name {
			return e, nil
		}
	}
	return nil, fmt.Errorf("VM %q not found", name)
}

// installPassDone reports whether a VM that was in "pending" install state
// has now completed its install pass (powered off, install_state promoted to
// "ready"). wasInstalling must be set by the caller before Start() is called.
func installPassDone(mgr *vm.Manager, name string, wasInstalling bool) bool {
	if !wasInstalling {
		return false
	}
	state, err := mgr.LoadState(name)
	if err != nil {
		return false
	}
	return !state.Running && state.InstallState == vm.InstallStateReady
}

// isInstalling reports whether the VM's install pass is still pending.
func isInstalling(mgr *vm.Manager, name string) bool {
	state, err := mgr.LoadState(name)
	if err != nil {
		return false
	}
	return state.InstallState == vm.InstallStatePending
}

// openQGAClient connects to the guest agent socket and returns the client plus
// a cleanup function. The caller must invoke close() when done.
func openQGAClient(socket string, timeout time.Duration) (*qemu.QGAClient, func(), error) {
	client, err := qemu.NewQGAClient(socket, timeout)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to guest agent: %w", err)
	}
	return client, func() { _ = client.Close() }, nil
}

// localIP returns the first non-loopback IPv4 address, falling back to 127.0.0.1.
func localIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() {
			continue
		}
		if ip = ip.To4(); ip != nil {
			return ip.String()
		}
	}
	return "127.0.0.1"
}

// freeLocalPort finds a free TCP port on localhost and returns it.
func freeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}
