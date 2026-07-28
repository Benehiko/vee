//go:build darwin

package vzhelper

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// hardenVZHelper heals a quarantined helper binary. A browser-downloaded
// release asset carries the com.apple.quarantine xattr, and Gatekeeper
// SIGKILLs quarantined ad-hoc binaries on exec — the same problem qemubin's
// hardenBinary solves for the QEMU bundle.
//
// The heal is deliberately narrow:
//   - No-op for non-quarantined helpers (the common curl/make paths pay one
//     cheap xattr probe, never a codesign).
//   - The quarantine strip is verified; if the attr survives (helper not
//     writable by this user, e.g. sudo-copied into /usr/local/bin) the error
//     is fatal to the start — proceeding would SIGKILL the helper at exec
//     with a misleading entitlement diagnostic.
//   - Re-signing happens only when the existing signature is invalid or
//     lacks the virtualization entitlement. A Developer-ID/notarized helper
//     runs fine after the xattr strip alone and must not be downgraded to
//     ad-hoc (that would also strip flags like hardened runtime).
//   - A file lock serializes concurrent starts: two racing codesign --force
//     invocations on one binary can leave a corrupt signature behind.
func harden(path string) error {
	xattr, err := exec.LookPath("xattr")
	if err != nil {
		return nil // no xattr tool, nothing to heal
	}
	if !helperQuarantined(xattr, path) {
		return nil
	}

	unlock, err := lockHarden()
	if err != nil {
		return err
	}
	defer unlock()

	// Re-check under the lock — another vee may have healed it already.
	if !helperQuarantined(xattr, path) {
		return nil
	}

	//nolint:noctx,gosec // fixed args on a vee-resolved helper path; no ctx in this call chain (mirrors qemubin hardenBinary)
	_ = exec.Command(xattr, "-d", "com.apple.quarantine", path).Run()
	if helperQuarantined(xattr, path) {
		return fmt.Errorf("%s is quarantined (browser download) and not writable by this user — Gatekeeper would kill it on start; clear it with: sudo xattr -d com.apple.quarantine %s", path, path)
	}

	// A valid signature that already carries the entitlement needs nothing
	// more; leave stronger-than-ad-hoc signatures untouched.
	if helperSignatureOK(path) {
		return nil
	}

	codesign, err := exec.LookPath("codesign")
	if err != nil {
		return fmt.Errorf("codesign not found: %w", err)
	}
	tmp, err := os.CreateTemp("", "vee-vz-entitlements-*.plist")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(Entitlements); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	//nolint:noctx,gosec // see above
	if out, err := exec.Command(codesign, "--force", "--sign", "-", "--timestamp=none",
		"--entitlements", tmp.Name(), path).CombinedOutput(); err != nil {
		return fmt.Errorf("codesign %s: %w\n%s", path, err, out)
	}
	return nil
}

// helperQuarantined probes the com.apple.quarantine xattr. Reading needs
// only read permission, so the probe works even where the strip cannot.
func helperQuarantined(xattr, path string) bool {
	//nolint:noctx,gosec // fixed args, vee-resolved path
	out, err := exec.Command(xattr, "-p", "com.apple.quarantine", path).Output()
	return err == nil && len(out) > 0
}

// helperSignatureOK reports whether the binary carries a valid signature
// that includes the virtualization entitlement.
func helperSignatureOK(path string) bool {
	codesign, err := exec.LookPath("codesign")
	if err != nil {
		return false
	}
	//nolint:noctx,gosec // fixed args, vee-resolved path
	if err := exec.Command(codesign, "--verify", path).Run(); err != nil {
		return false
	}
	//nolint:noctx,gosec // fixed args, vee-resolved path
	out, err := exec.Command(codesign, "-d", "--entitlements", "-", path).CombinedOutput()
	return err == nil && strings.Contains(string(out), "com.apple.security.virtualization")
}

// lockHarden serializes the strip+re-sign sequence across vee processes.
// Two concurrent codesign --force runs on one binary can leave a corrupt
// signature that fails every later start (and, quarantine already stripped,
// would never be re-healed).
func lockHarden() (func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".vee")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "vz-harden.lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // vee-owned lock file
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
