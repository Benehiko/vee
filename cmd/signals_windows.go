//go:build windows

package cmd

import "os"

// stopSignals returns the OS signals that should cause an interactive,
// long-running command (tunnels, ssh-share) to shut down cleanly. Windows does
// not deliver SIGTERM to Go programs, so only os.Interrupt (Ctrl-C) is
// registered.
func stopSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
