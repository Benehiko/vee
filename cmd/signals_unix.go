//go:build !windows

package cmd

import (
	"os"
	"syscall"
)

// stopSignals returns the OS signals that should cause an interactive,
// long-running command (tunnels, ssh-share) to shut down cleanly. On unix that
// is SIGINT (Ctrl-C) and SIGTERM (service stop / kill).
func stopSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
}
