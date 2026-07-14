//go:build windows

package virtiofs

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// setDetachAttrs configures cmd so the launched process survives the parent
// exiting. virtiofsd is Linux-only (gated by platform.SupportsVirtiofsd), so
// this path is not exercised on Windows, but is provided so the package builds.
func setDetachAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}
