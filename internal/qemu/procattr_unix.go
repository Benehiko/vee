//go:build !windows

package qemu

import (
	"os"
	"os/exec"
	"syscall"
)

// setDetachAttrs configures cmd so the launched QEMU survives the parent
// process (and terminal) exiting. On unix this starts a new session via
// setsid(2), detaching the child from the controlling terminal and process
// group.
func setDetachAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// processAlive reports whether the process with the given PID is still running.
// On unix it sends signal 0, which performs the kernel's permission/existence
// check without delivering an actual signal.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
