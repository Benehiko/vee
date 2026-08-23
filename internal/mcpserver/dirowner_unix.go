//go:build !windows

package mcpserver

import (
	"fmt"
	"os"
	"syscall"
)

// dirOwner reports the UID and GID owning dir on the host. See
// templates.BitmagnetOptions for why the bitmagnet template needs them.
func dirOwner(dir string) (uid, gid int, err error) {
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
