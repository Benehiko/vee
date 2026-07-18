package qemubin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSonameResolvableFindsBinDirLink(t *testing.T) {
	binDir := t.TempDir()
	// A file placed next to the binary counts as resolvable via $ORIGIN rpath.
	link := filepath.Join(binDir, "libaio.so.1t64")
	if err := os.WriteFile(link, []byte("stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !sonameResolvable("libaio.so.1t64", binDir) {
		t.Error("expected soname next to binary to be resolvable")
	}
	if sonameResolvable("libdefinitely-missing.so.99t64", binDir) {
		t.Error("expected a nonexistent soname to be unresolvable")
	}
}

func TestFindHostLibMissing(t *testing.T) {
	if got := findHostLib("libdefinitely-missing.so.99t64"); got != "" {
		t.Errorf("expected empty path for missing lib, got %q", got)
	}
}

// TestEnsureSonameCompatCreatesLink drives the healing path end-to-end using a
// fake "binary" whose DT_NEEDED we can't set, so instead it exercises the branch
// where the host already resolves everything: ensureSonameCompat against the
// real installed QEMU (if present) must not error and must leave a working link
// for any t64 soname the host lacks.
func TestEnsureSonameCompatOnInstalledBinary(t *testing.T) {
	binPath, err := BinPath()
	if err != nil {
		t.Skip("no bin path")
	}
	if _, err := os.Stat(binPath); err != nil {
		t.Skip("managed QEMU not installed; nothing to heal")
	}
	if err := ensureSonameCompat(binPath); err != nil {
		t.Fatalf("ensureSonameCompat: %v", err)
	}
	// After healing, every t64 DT_NEEDED must be resolvable next to the binary
	// or on the host — otherwise QEMU would still fail to load.
	needed, err := elfNeeded(binPath)
	if err != nil {
		t.Fatalf("elfNeeded: %v", err)
	}
	binDir := filepath.Dir(binPath)
	for _, so := range needed {
		if !sonameResolvable(so, binDir) {
			t.Errorf("soname %q still unresolvable after ensureSonameCompat", so)
		}
	}
}
