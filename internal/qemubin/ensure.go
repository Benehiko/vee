// Package qemubin manages the vee-custom QEMU binary.
//
// vee ships a custom QEMU build with OpenGL/virgl/virtigl enabled so gaming and
// graphical VMs get hardware-accelerated display without requiring the host to
// have a specially compiled system QEMU. The binary is published as a GitHub
// Release asset and downloaded to ~/.vee/bin/ on demand.
//
// The binary is host-architecture specific: qemu-system-aarch64 on Apple
// Silicon, qemu-system-x86_64 on amd64. When no vee-managed asset is published
// for the current platform, Ensure falls back to a system/third-party QEMU —
// on macOS that means probing Homebrew and a qemu-virgl tap (or UTM), which
// provide the virglrenderer-capable QEMU needed for accelerated virtio-gpu.
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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Benehiko/vee/internal/platform"
)

// qemuBinaryName returns the qemu-system binary name for the host's native
// guest architecture (e.g. qemu-system-aarch64 on Apple Silicon).
func qemuBinaryName() string {
	return platform.DefaultQemuBinaryName()
}

// BinPath returns the expected install path for the managed QEMU binary.
func BinPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".vee", "bin", qemuBinaryName()), nil
}

// Ensure checks whether the managed QEMU binary exists at ~/.vee/bin/ and
// matches the pinned version marker. If not, it downloads, verifies, and
// installs it. Returns the absolute path to the binary on success.
//
// If the GitHub release asset is not yet available (empty checksum in
// Checksums) the function falls back to a system/third-party QEMU via
// resolveSystemQemu and prints a notice.
func Ensure() (string, error) {
	binPath, err := BinPath()
	if err != nil {
		return "", err
	}

	marker := binPath + ".version"

	// Check if already installed at the pinned version.
	if b, err := os.ReadFile(marker); err == nil && string(b) == PinnedVersion { //nolint:gosec // marker path derived from UserHomeDir + fixed name, not user input
		if _, err := os.Stat(binPath); err == nil {
			return binPath, nil
		}
	}

	goos := runtime.GOOS
	goarch := runtime.GOARCH

	expectedSum := Checksums[goos+"-"+goarch]
	if expectedSum == "" {
		// No release asset yet — fall back to a system/third-party QEMU.
		resolved, rerr := resolveSystemQemu()
		if rerr != nil {
			return "", rerr
		}
		fmt.Fprintf(os.Stderr, "vee-qemu %s not yet published for %s/%s; using %s\n", PinnedVersion, goos, goarch, resolved)
		return resolved, nil
	}

	fmt.Fprintf(os.Stderr, "Downloading vee-qemu %s for %s/%s…\n", PinnedVersion, goos, goarch)

	asset := AssetName(goos, goarch)
	url := fmt.Sprintf("%s/%s/%s", releaseBaseURL, PinnedVersion, asset)

	// vee-qemu assets are self-contained bundles (bin/, lib/, share/) extracted
	// into ~/.vee so the binary finds its bundled dylibs (ANGLE/virglrenderer/
	// MoltenVK on macOS) and firmware. binPath is ~/.vee/bin/<name>.
	veeRoot := filepath.Dir(filepath.Dir(binPath))
	if err := downloadAndInstall(url, expectedSum, veeRoot); err != nil {
		return "", fmt.Errorf("install vee-qemu: %w", err)
	}
	if _, err := os.Stat(binPath); err != nil {
		return "", fmt.Errorf("install vee-qemu: %s missing after extraction", binPath)
	}

	// macOS: strip the download quarantine and (re-)apply the hypervisor
	// entitlement so HVF works. No-op on other hosts.
	if err := hardenBinary(binPath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not prepare %s for HVF: %v\n", binPath, err)
	}

	// Write version marker.
	if err := os.WriteFile(marker, []byte(PinnedVersion), 0o600); err != nil {
		return "", fmt.Errorf("write version marker: %w", err)
	}

	fmt.Fprintf(os.Stderr, "vee-qemu installed at %s\n", binPath)
	return binPath, nil
}

// resolveSystemQemu locates a usable qemu-system binary when no vee-managed
// build is published for the current platform. On macOS it probes common
// Homebrew prefixes and PATH; stock Homebrew QEMU works for basic use but does
// software rendering only — accelerated virtio-gpu requires a virgl-capable
// build (a qemu-virgl tap, UTM's bundled QEMU, or a future vee-qemu asset),
// which is surfaced in the error guidance when nothing is found.
func resolveSystemQemu() (string, error) {
	name := qemuBinaryName()

	// A non-versioned drop-in under ~/.vee/bin takes precedence.
	if home, err := os.UserHomeDir(); err == nil {
		vee := filepath.Join(home, ".vee", "bin", name)
		if _, err := os.Stat(vee); err == nil {
			return vee, nil
		}
	}

	if runtime.GOOS == "darwin" {
		for _, c := range []string{
			"/opt/homebrew/bin/" + name, // Apple Silicon Homebrew (incl. qemu-virgl taps)
			"/usr/local/bin/" + name,    // Intel Homebrew
		} {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("no %s found on this macOS host. Install QEMU "+
			"(e.g. `brew install qemu` for basic use, or a qemu-virgl tap such as "+
			"knazarov/qemu-virgl — or UTM — for accelerated virtio-gpu), or wait "+
			"for a published vee-qemu %s build", name, PinnedVersion)
	}

	// Linux and others: rely on PATH; QEMU surfaces a clear error later if absent.
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return name, nil
}

func downloadAndInstall(url, expectedSHA256, destRoot string) error {
	if err := os.MkdirAll(destRoot, 0o750); err != nil {
		return err
	}

	resp, err := http.Get(url) //nolint:noctx,gosec // one-shot download from pinned GitHub release URL (releaseBaseURL + PinnedVersion), not attacker-controlled
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	// Stream into a temp file while computing the SHA-256.
	tmp, err := os.CreateTemp(destRoot, ".qemu-download-*")
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

	// Extract the whole bundle (bin/, lib/, share/) into destRoot.
	if err := extractBundle(tmpName, destRoot); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	return nil
}

// extractBundle extracts every entry of a .tar.gz into destRoot, preserving the
// archive's directory layout (bin/, lib/, share/), file modes, and symlinks. It
// rejects entries whose path would escape destRoot.
func extractBundle(tarGzPath, destRoot string) error {
	f, err := os.Open(tarGzPath) //nolint:gosec // tarGzPath is a temp file created by this package via os.CreateTemp, not user input
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

		name := filepath.Clean(hdr.Name)
		if name == "." {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		target := filepath.Join(destRoot, name)
		if rel, err := filepath.Rel(destRoot, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			//nolint:gosec // preserve archive file modes: the QEMU binary and its .dylibs need the exec bit; the bundle is checksum-verified before extraction
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil { //nolint:gosec // trusted, checksum-verified asset
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			// Skip other entry types (devices, fifos, etc.).
		}
	}
	return nil
}
