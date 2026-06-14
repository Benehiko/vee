package qemubin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// makeTarGz writes a .tar.gz at path containing the given entries
// (name -> contents). A name ending in "/" creates a directory entry.
func makeTarGz(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if len(name) > 0 && name[len(name)-1] == '/' {
			if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
				t.Fatalf("write dir header: %v", err)
			}
			continue
		}
		hdr := &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write tar.gz: %v", err)
	}
}

func TestExtractBundleLayout(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "bundle.tar.gz")
	makeTarGz(t, archive, map[string]string{
		"bin/":                            "",
		"bin/qemu-system-aarch64":         "#!/bin/sh\n",
		"lib/libEGL.dylib":                "angle",
		"share/qemu/edk2-aarch64-code.fd": "firmware",
	})

	dest := filepath.Join(tmp, "root")
	if err := extractBundle(archive, dest); err != nil {
		t.Fatalf("extractBundle: %v", err)
	}

	bin := filepath.Join(dest, "bin", "qemu-system-aarch64")
	fi, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("binary not extracted: %v", err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("binary should be executable, got mode %v", fi.Mode())
	}
	for _, p := range []string{
		filepath.Join(dest, "lib", "libEGL.dylib"),
		filepath.Join(dest, "share", "qemu", "edk2-aarch64-code.fd"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}
}

func TestExtractBundleRejectsTraversal(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "evil.tar.gz")
	makeTarGz(t, archive, map[string]string{
		"../escape.txt": "pwned",
	})

	dest := filepath.Join(tmp, "root")
	if err := extractBundle(archive, dest); err == nil {
		t.Fatal("expected extractBundle to reject a path-traversal entry")
	}
	if _, err := os.Stat(filepath.Join(tmp, "escape.txt")); err == nil {
		t.Fatal("traversal entry escaped destRoot")
	}
}

func TestAssetNameArchAware(t *testing.T) {
	if got := AssetName("darwin", "arm64"); got != "qemu-system-aarch64-darwin-arm64.tar.gz" {
		t.Errorf("darwin/arm64 asset: got %q", got)
	}
	if got := AssetName("linux", "amd64"); got != "qemu-system-x86_64-linux-amd64.tar.gz" {
		t.Errorf("linux/amd64 asset: got %q", got)
	}
}
