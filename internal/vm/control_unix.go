//go:build !windows

package vm

import "path/filepath"

// controlSocketAddr returns the address vee uses to reach a QEMU control
// channel (QMP or the guest agent). On unix this is an AF_UNIX socket path
// inside the VM directory, e.g. "<vmDir>/qmp.sock".
func controlSocketAddr(vmDir, base string) (string, error) {
	return filepath.Join(vmDir, base+".sock"), nil
}
