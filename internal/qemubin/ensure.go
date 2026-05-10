// Package qemubin manages the vee-custom QEMU binary.
//
// vee ships a custom QEMU build with OpenGL/virgl/virtigl enabled so gaming
// VMs get hardware-accelerated display without requiring the host to have a
// specially compiled system QEMU.  The binary is published as a GitHub Release
// asset and downloaded to ~/.vee/bin/ on demand.
package qemubin

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

const binaryName = "qemu-system-x86_64"

// BinPath returns the expected install path for the managed QEMU binary.
func BinPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".vee", "bin", binaryName), nil
}

// Ensure checks whether the managed QEMU binary exists at ~/.vee/bin/ and
// matches the pinned version marker.  If not, it downloads, verifies, and
// installs it.  Returns the absolute path to the binary on success.
//
// If the GitHub release asset is not yet available (empty checksum in
// Checksums) the function falls back to the system qemu-system-x86_64 and
// prints a notice.
func Ensure() (string, error) {
	binPath, err := BinPath()
	if err != nil {
		return "", err
	}

	marker := binPath + ".version"

	// Check if already installed at the pinned version.
	if b, err := os.ReadFile(marker); err == nil && string(b) == PinnedVersion {
		if _, err := os.Stat(binPath); err == nil {
			return binPath, nil
		}
	}

	goos := runtime.GOOS
	goarch := runtime.GOARCH

	expectedSum := Checksums[goos+"-"+goarch]
	if expectedSum == "" {
		// No release asset yet — fall back to system QEMU.
		fmt.Fprintf(os.Stderr, "vee-qemu %s not yet published for %s/%s; using system qemu-system-x86_64\n", PinnedVersion, goos, goarch)
		return "qemu-system-x86_64", nil
	}

	fmt.Fprintf(os.Stderr, "Downloading vee-qemu %s for %s/%s…\n", PinnedVersion, goos, goarch)

	asset := AssetName(goos, goarch)
	url := fmt.Sprintf("%s/%s/%s", releaseBaseURL, PinnedVersion, asset)

	if err := downloadAndInstall(url, expectedSum, binPath); err != nil {
		return "", fmt.Errorf("install vee-qemu: %w", err)
	}

	// Write version marker.
	if err := os.WriteFile(marker, []byte(PinnedVersion), 0o644); err != nil {
		return "", fmt.Errorf("write version marker: %w", err)
	}

	fmt.Fprintf(os.Stderr, "vee-qemu installed at %s\n", binPath)
	return binPath, nil
}

func downloadAndInstall(url, expectedSHA256, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}

	resp, err := http.Get(url) //nolint:noctx // simple one-shot download
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	// Stream into a temp file while computing the SHA-256.
	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".qemu-download-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	_ = tmp.Close()

	got := hex.EncodeToString(h.Sum(nil))
	if got != expectedSHA256 {
		return fmt.Errorf("SHA-256 mismatch: expected %s got %s", expectedSHA256, got)
	}

	// Extract the binary from the .tar.gz.
	if err := extractBinary(tmpName, binaryName, destPath); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	return nil
}

func extractBinary(tarGzPath, binaryName, destPath string) error {
	f, err := os.Open(tarGzPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) != binaryName {
			continue
		}
		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil { //nolint:gosec // trusted release asset
			_ = out.Close()
			return err
		}
		return out.Close()
	}
	return fmt.Errorf("%s not found in archive", binaryName)
}
