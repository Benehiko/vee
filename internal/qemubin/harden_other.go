//go:build !darwin

package qemubin

// hardenBinary is a no-op on non-macOS hosts. Quarantine xattrs and the
// hypervisor codesign entitlement are macOS-specific concerns.
func hardenBinary(_ string) error { return nil }
