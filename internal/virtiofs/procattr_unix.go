//go:build !windows

package virtiofs

import (
	"os/exec"
	"syscall"
)

// setDetachAttrs configures cmd so the launched virtiofsd survives the parent
// process exiting, via setsid(2).
func setDetachAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
