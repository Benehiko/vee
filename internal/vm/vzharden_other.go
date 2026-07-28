//go:build !darwin

package vm

// hardenVZHelper is darwin-only (quarantine xattr + codesign); no-op elsewhere.
func hardenVZHelper(string) error { return nil }
