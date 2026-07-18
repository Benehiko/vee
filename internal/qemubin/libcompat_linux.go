package qemubin

import (
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ensureSonameCompat heals a class of runtime-loader failures caused by
// Debian/Ubuntu's 64-bit time_t transition, where several shared libraries were
// renamed with a "t64" soname suffix (e.g. libaio.so.1 -> libaio.so.1t64). The
// ABI on 64-bit architectures is unchanged — only the filename differs — so a
// QEMU binary built on a Debian-family host records a DT_NEEDED of
// "libaio.so.1t64" that non-Debian distros (Arch, Fedora, …) cannot satisfy,
// since they ship the plain "libaio.so.1".
//
// For each DT_NEEDED soname ending in "t64" that the host cannot resolve, if the
// host provides the un-suffixed variant, a compat symlink is created next to the
// QEMU binary (in ~/.vee/bin, which is on the binary's $ORIGIN rpath / load
// path) pointing the Debian soname at the host library. This is best-effort:
// any failure is returned so the caller can log and continue — QEMU will still
// surface its own loader error at launch if the shim was needed but not created.
//
// binPath is the managed QEMU binary; binDir is its directory (the symlink
// destination). See https://github.com/Benehiko/vee/issues/40.
func ensureSonameCompat(binPath string) error {
	binDir := filepath.Dir(binPath)

	needed, err := elfNeeded(binPath)
	if err != nil {
		return fmt.Errorf("read ELF needed libs: %w", err)
	}

	for _, soname := range needed {
		base, ok := strings.CutSuffix(soname, "t64")
		if !ok {
			continue
		}
		// Already resolvable (host provides it, or a prior run created the
		// compat link)? Then nothing to do for this soname.
		if sonameResolvable(soname, binDir) {
			continue
		}
		// Locate the host's un-suffixed library to point the compat link at.
		target := findHostLib(base)
		if target == "" {
			continue
		}
		link := filepath.Join(binDir, soname)
		// Recreate the link so a stale/dangling one gets refreshed.
		_ = os.Remove(link)
		if err := os.Symlink(target, link); err != nil {
			return fmt.Errorf("create %s -> %s compat symlink: %w", soname, target, err)
		}
	}
	return nil
}

// elfNeeded returns the DT_NEEDED shared-library sonames of an ELF binary.
func elfNeeded(path string) ([]string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.ImportedLibraries()
}

// sonameResolvable reports whether a shared-library soname can be found either
// next to the binary (binDir, on its rpath) or in the standard system library
// directories.
func sonameResolvable(soname, binDir string) bool {
	if _, err := os.Stat(filepath.Join(binDir, soname)); err == nil {
		return true
	}
	return findHostLib(soname) != ""
}

// findHostLib returns the path to a shared library with the given soname in the
// common system library directories, or "" if not present.
func findHostLib(soname string) string {
	for _, dir := range []string{
		"/usr/lib",
		"/usr/lib64",
		"/lib",
		"/lib64",
		"/usr/lib/x86_64-linux-gnu",
		"/usr/lib/aarch64-linux-gnu",
	} {
		candidate := filepath.Join(dir, soname)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}
