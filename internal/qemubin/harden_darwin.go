//go:build darwin

package qemubin

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// entitlementsPlist is the codesign entitlements applied to the QEMU binary so
// HVF (com.apple.security.hypervisor) and the vmapple/apple-gfx path
// (com.apple.security.virtualization) work. macOS honors these entitlements for
// ad-hoc signatures, so no paid Apple Developer certificate is required.
//
//go:embed qemu-entitlements.plist
var entitlementsPlist []byte

// hardenBinary prepares a freshly extracted QEMU binary for use on macOS:
//   - strips the com.apple.quarantine xattr from the whole bundle so Gatekeeper
//     does not block exec of the binary or its bundled dylibs, and
//   - (re-)applies an ad-hoc code signature carrying the hypervisor entitlement,
//     which is mandatory for -accel hvf.
//
// Re-signing is done last (after extraction) so it is valid regardless of how
// the asset was produced or whether a user dropped in their own binary.
func hardenBinary(binPath string) error {
	veeRoot := filepath.Dir(filepath.Dir(binPath))

	// Best-effort quarantine removal (ignore errors: the xattr may be absent).
	if _, err := exec.LookPath("xattr"); err == nil {
		_ = exec.Command("xattr", "-dr", "com.apple.quarantine", veeRoot).Run()
	}

	codesign, err := exec.LookPath("codesign")
	if err != nil {
		return fmt.Errorf("codesign not found — install the Xcode command line tools; " +
			"HVF requires the com.apple.security.hypervisor entitlement")
	}

	tmp, err := os.CreateTemp("", "vee-qemu-entitlements-*.plist")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(entitlementsPlist); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	out, err := exec.Command(codesign,
		"--force", "--sign", "-",
		"--entitlements", tmp.Name(),
		"--timestamp=none",
		binPath,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("codesign %s: %w: %s", binPath, err, string(out))
	}
	return nil
}
