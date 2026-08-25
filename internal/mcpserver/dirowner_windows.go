//go:build windows

package mcpserver

import "fmt"

// dirOwner is unimplemented on Windows: vee's virtiofs shares require a Linux
// host, so a bind-mounted PostgreSQL data directory is unreachable there.
func dirOwner(dir string) (uid, gid int, err error) {
	return 0, 0, fmt.Errorf("pg_data_dir is not supported on Windows hosts (virtiofs shares require a Linux host)")
}
