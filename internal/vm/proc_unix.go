//go:build !windows

package vm

import (
	"os"
	"syscall"
)

// processAlive reports whether the process with the given PID is still running.
// On unix it sends signal 0 (existence check without delivering a signal).
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// terminateProcess asks a process to shut down gracefully. On unix this is
// SIGTERM, letting the target run its shutdown handlers.
func terminateProcess(proc *os.Process) error {
	return proc.Signal(syscall.SIGTERM)
}
