//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"syscall"
)

// ownerOf reports the UID and GID owning dir on the host.
//
// The bitmagnet template needs these because virtiofs presents host ownership
// to the guest unchanged, and vee runs virtiofsd unprivileged — so the guest
// cannot chown the share to its own postgres user. The guest's postgres
// account is renumbered to these IDs instead.
func ownerOf(dir string) (uid, gid int, err error) {
	info, err := os.Stat(dir)
	if err != nil {
		return 0, 0, fmt.Errorf("stat PostgreSQL data directory: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("cannot determine owner of %s on this platform", dir)
	}
	return int(st.Uid), int(st.Gid), nil
}
