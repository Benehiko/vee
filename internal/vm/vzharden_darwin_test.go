//go:build darwin

package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testExec runs a host tool (codesign/xattr) against test-owned temp files.
//
//nolint:gosec // fixed tools, temp-file args created by the test itself
func testExec(t *testing.T, name string, args ...string) ([]byte, error) {
	t.Helper()
	return exec.CommandContext(t.Context(), name, args...).CombinedOutput()
}

// quarantined probes the quarantine xattr; xattr -p exits non-zero (with the
// error on stderr) when the attr is absent, so key on the exit status.
func quarantined(t *testing.T, path string) bool {
	t.Helper()
	_, err := testExec(t, "xattr", "-p", "com.apple.quarantine", path)
	return err == nil
}

// TestHardenVZHelper exercises the real quarantine-heal path: a quarantined
// Mach-O (a copy of the test binary) must come out un-quarantined and signed
// with the virtualization entitlement.
func TestHardenVZHelper(t *testing.T) {
	target := copyTestBinary(t)

	if out, err := testExec(t, "xattr", "-w", "com.apple.quarantine", "0083;00000000;TestBrowser;", target); err != nil {
		t.Fatalf("set quarantine: %v\n%s", err, out)
	}
	if !quarantined(t, target) {
		t.Fatal("quarantine xattr did not stick")
	}

	if err := hardenVZHelper(target); err != nil {
		t.Fatalf("hardenVZHelper: %v", err)
	}
	if quarantined(t, target) {
		t.Error("quarantine xattr survived")
	}
	out, err := testExec(t, "codesign", "-d", "--entitlements", "-", target)
	if err != nil {
		t.Fatalf("codesign -d: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "com.apple.security.virtualization") {
		t.Errorf("entitlement missing after harden:\n%s", out)
	}

	// Non-quarantined binaries are a no-op (no codesign cost on every start).
	if err := hardenVZHelper(target); err != nil {
		t.Fatalf("hardenVZHelper (clean): %v", err)
	}
}

// TestHardenVZHelperPreservesValidSignature: a quarantined helper whose
// signature is already valid and entitled must only lose the xattr — never
// be re-signed (a Developer-ID signature would be downgraded to ad-hoc).
func TestHardenVZHelperPreservesValidSignature(t *testing.T) {
	target := copyTestBinary(t)

	// Pre-sign with the entitlement (what a release build ships).
	ents := filepath.Join(t.TempDir(), "ents.plist")
	if err := os.WriteFile(ents, entitlementsForTest(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := testExec(t, "codesign", "--force", "--sign", "-", "--timestamp=none",
		"--entitlements", ents, target); err != nil {
		t.Fatalf("pre-sign: %v\n%s", err, out)
	}
	before := fileDigest(t, target)

	if out, err := testExec(t, "xattr", "-w", "com.apple.quarantine", "0083;00000000;TestBrowser;", target); err != nil {
		t.Fatalf("set quarantine: %v\n%s", err, out)
	}
	if err := hardenVZHelper(target); err != nil {
		t.Fatalf("hardenVZHelper: %v", err)
	}
	if quarantined(t, target) {
		t.Error("quarantine xattr survived")
	}
	if after := fileDigest(t, target); after != before {
		t.Errorf("valid entitled signature was replaced: file digest changed %s -> %s", before, after)
	}
}

// TestHardenVZHelperUnwritable: when the quarantine cannot be stripped
// (helper not writable by this user), the heal must fail hard with an
// actionable message — proceeding would SIGKILL the helper at exec.
func TestHardenVZHelperUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can always strip xattrs")
	}
	target := copyTestBinary(t)
	if out, err := testExec(t, "xattr", "-w", "com.apple.quarantine", "0083;00000000;TestBrowser;", target); err != nil {
		t.Fatalf("set quarantine: %v\n%s", err, out)
	}
	if err := os.Chmod(target, 0o555); err != nil { //nolint:gosec // read-only+exec is the scenario under test
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0o755) }) //nolint:gosec // restore exec perms so TempDir cleanup works

	err := hardenVZHelper(target)
	if err == nil || !strings.Contains(err.Error(), "sudo xattr") {
		t.Fatalf("expected hard failure with sudo xattr guidance, got: %v", err)
	}
}

func copyTestBinary(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"codesign", "xattr"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self) //nolint:gosec // the test binary's own path
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(target, data, 0o755); err != nil { //nolint:gosec // test helper must be executable
		t.Fatal(err)
	}
	return target
}

func entitlementsForTest(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "vzhelper", "vz.entitlements"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fileDigest hashes the file bytes — the strongest "signature untouched"
// assertion (codesign rewrites the whole Mach-O when it re-signs).
func fileDigest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test-owned temp file
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
