//go:build windows

package qemu

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// setDetachAttrs configures cmd so the launched QEMU survives the parent
// process (and console) exiting. Windows has no setsid(2); the equivalent is a
// new process group detached from the parent console. DETACHED_PROCESS ensures
// the child does not inherit or attach to the parent's console.
func setDetachAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}

// processAlive reports whether the process with the given PID is still running.
// Windows has no signal-0 existence check, so open a handle and inspect the
// exit code: a live process reports STILL_ACTIVE (259).
func processAlive(pid int) bool {
	const stillActive = 259
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
