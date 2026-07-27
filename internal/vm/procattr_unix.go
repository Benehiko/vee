//go:build !windows

package vm

import (
	"os/exec"
	"syscall"
)

// setDetachAttrs configures cmd so the launched helper survives the parent
// process exiting, via setsid(2). Mirrors the QEMU/virtiofsd launch pattern.
func setDetachAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
