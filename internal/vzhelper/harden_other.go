//go:build !darwin

package vzhelper

// harden is darwin-only (quarantine xattr + codesign); no-op elsewhere.
func harden(string) error { return nil }
