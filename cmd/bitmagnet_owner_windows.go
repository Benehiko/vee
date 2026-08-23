//go:build windows

package cmd

import "fmt"

// ownerOf is unimplemented on Windows: virtiofs shares are a Linux-host
// feature in vee (see platform.SupportsVirtiofsd), so the bitmagnet template's
// bind-mounted data directory cannot be used from a Windows host anyway.
func ownerOf(dir string) (uid, gid int, err error) {
	return 0, 0, fmt.Errorf("--pg-data-dir is not supported on Windows hosts (virtiofs shares require a Linux host)")
}
