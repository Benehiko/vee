//go:build !linux

package qemubin

// ensureSonameCompat is a no-op off Linux: the Debian time_t soname transition
// (libaio.so.1 -> libaio.so.1t64) only affects Linux ELF dynamic linking.
// macOS and Windows bundles carry their own dylibs/DLLs.
func ensureSonameCompat(_ string) error {
	return nil
}
