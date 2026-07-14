//go:build windows

package vm

import (
	"os"

	"golang.org/x/sys/windows"
)

// processAlive reports whether the process with the given PID is still running.
// Windows has no signal-0 check, so open a handle and inspect the exit code: a
// live process reports STILL_ACTIVE (259).
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

// terminateProcess asks a process to shut down. Windows has no SIGTERM; the
// closest portable action is a hard terminate via os.Process.Kill
// (TerminateProcess). Graceful guest shutdown is handled upstream via QMP
// system_powerdown, so this only affects helper processes.
func terminateProcess(proc *os.Process) error {
	return proc.Kill()
}
