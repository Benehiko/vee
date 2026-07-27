//go:build windows

package vm

import (
	"os/exec"
	"syscall"
)

// setDetachAttrs configures cmd so the launched helper survives the parent
// process exiting. On Windows this detaches the process from the console.
func setDetachAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // DETACHED_PROCESS
	}
}
