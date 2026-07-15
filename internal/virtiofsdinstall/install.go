package virtiofsdinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

const (
	// virtiofsdVersion is the human-facing tag, used only in log lines.
	virtiofsdVersion = "v1.13.3"

	// virtiofsdCommit is the git commit that tag v1.13.3 points at. We fetch the
	// commit-SHA archive rather than the tag archive because a commit SHA names an
	// immutable git tree, so GitLab produces a byte-reproducible tarball — the tag
	// archive's gzip is not guaranteed stable, which would break a pinned hash.
	virtiofsdCommit = "bbf82173682a3e48083771a0a23331e5c23b4924"

	// virtiofsdSHA256 is the sha256 of the commit-SHA source archive below.
	// Bump this deliberately (with virtiofsdCommit) when upgrading virtiofsd.
	virtiofsdSHA256 = "76ee2a54eb76ef1ab130790a93970a2f6e26ff1de5589dd5aa2a102b1a0bec92"

	virtiofsdSrcURL = "https://gitlab.com/virtio-fs/virtiofsd/-/archive/" + virtiofsdCommit + "/virtiofsd-" + virtiofsdCommit + ".tar.gz"
)

// ErrNoContainerRuntime is returned by EnsureVirtiofsd when no host container
// runtime (nerdctl/docker) is available to build virtiofsd. Callers can detect
// this with errors.Is to fall back to an alternative build strategy (e.g.
// compiling inside a VM) instead of giving up.
var ErrNoContainerRuntime = errors.New("no container runtime found (nerdctl/docker required to build virtiofsd)")

// SourceURL returns the pinned virtiofsd source-archive URL, so alternative
// builders (e.g. the in-VM fallback) fetch the exact same tarball.
func SourceURL() string { return virtiofsdSrcURL }

// SourceSHA256 returns the expected sha256 (hex) of the source archive at
// SourceURL, so alternative builders verify the same integrity guarantee.
func SourceSHA256() string { return virtiofsdSHA256 }

// Version returns the human-facing virtiofsd version tag.
func Version() string { return virtiofsdVersion }

// EnsureVirtiofsd ensures that a virtiofsd binary exists at ~/.vee/bin/virtiofsd.
// If already present it returns immediately. Otherwise it downloads the source
// tarball on the host and compiles it inside a container (vessel → nerdctl → docker).
func EnsureVirtiofsd(home string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("virtiofsd is only supported on Linux")
	}

	binDir := filepath.Join(home, ".vee", "bin")
	dst := filepath.Join(binDir, "virtiofsd")

	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}

	if err := os.MkdirAll(binDir, 0o750); err != nil {
		return "", fmt.Errorf("create bin dir: %w", err)
	}

	containerRuntime, err := findContainerRuntime()
	if err != nil {
		return "", err
	}

	fmt.Fprintf(os.Stderr, "virtiofsd not found — downloading and building %s (this takes a few minutes)…\n", virtiofsdVersion)

	// Download tarball on the host (has network access).
	buildDir := filepath.Join(os.TempDir(), "virtiofsd-build")
	if err := os.MkdirAll(buildDir, 0o750); err != nil {
		return "", fmt.Errorf("create build dir: %w", err)
	}
	tarPath := filepath.Join(buildDir, "virtiofsd.tar.gz")

	if err := downloadFile(tarPath, virtiofsdSrcURL); err != nil {
		return "", fmt.Errorf("download virtiofsd source: %w", err)
	}
	if err := verifyChecksum(tarPath, virtiofsdSHA256); err != nil {
		// Drop the bad tarball so the next run re-downloads instead of trusting it.
		_ = os.Remove(tarPath)
		return "", fmt.Errorf("verify virtiofsd source: %w", err)
	}

	// Build inside a glibc container — source tarball and output dir are
	// bind-mounted. Cargo needs network access to fetch the crates registry on
	// first build. libseccomp and libcap-ng are required link dependencies.
	//
	// Use a glibc toolchain (rust:bookworm), NOT rust:alpine: the musl build of
	// virtiofsd 1.13.x segfaults at startup on a glibc host (it creates its
	// listening socket, then dies), so the daemon never serves the share. The
	// distro-packaged virtiofsd is glibc for the same reason.
	buildScript := `set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq libseccomp-dev libcap-ng-dev
[ -f /build/Cargo.toml ] || tar -xz --strip-components=1 -f /src/virtiofsd.tar.gz -C /build
cd /build
cargo build --release
cp target/release/virtiofsd /out/virtiofsd
`
	args := []string{
		"run", "--rm",
		"-v", buildDir + ":/src:ro",
		"-v", buildDir + ":/build",
		"-v", binDir + ":/out",
		"rust:bookworm",
		"sh", "-c", buildScript,
	}

	//nolint:gosec // G204: containerRuntime is resolved via exec.LookPath from a fixed allowlist (nerdctl/docker); args are internally constructed, not user input.
	cmd := exec.CommandContext(context.Background(), containerRuntime, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("virtiofsd build failed: %w", err)
	}

	// virtiofsd is an executable; it needs the exec bit, so perms cannot be 0o600 or less.
	if err := os.Chmod(dst, 0o750); err != nil { //nolint:gosec // G302: executable binary requires the exec bit; 0o750 is the tightest workable mode.
		return "", fmt.Errorf("chmod virtiofsd: %w", err)
	}

	fmt.Fprintf(os.Stderr, "virtiofsd built: %s\n", dst)
	return dst, nil
}

func downloadFile(dst, url string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil // already downloaded
	}
	hc := &http.Client{Timeout: 5 * time.Minute}
	resp, err := hc.Get(url) //nolint:noctx
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, url)
	}
	f, err := os.Create(dst) //nolint:gosec // G304: dst is an internally constructed path under os.TempDir(), not user-controlled.
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(f, resp.Body)
	return err
}

// verifyChecksum computes the sha256 of the file at path and compares it, in
// constant-string form, against wantHex. On mismatch it returns an error naming
// both the expected and actual hashes, so an intentional upstream re-roll can be
// reconciled by bumping the pinned constant.
func verifyChecksum(path, wantHex string) error {
	f, err := os.Open(path) //nolint:gosec // G304: path is an internally constructed tempdir path, not user input.
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	gotHex := hex.EncodeToString(h.Sum(nil))
	if gotHex != wantHex {
		return fmt.Errorf("sha256 mismatch: expected %s, got %s", wantHex, gotHex)
	}
	return nil
}

// findContainerRuntime returns the path to the first available container runtime
// that supports network access inside containers (needed for cargo to fetch crates).
// vessel is excluded because its containers lack DNS resolution. Returns
// ErrNoContainerRuntime when none is found, so callers can fall back to an
// in-VM build.
func findContainerRuntime() (string, error) {
	for _, name := range []string{"nerdctl", "docker"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", ErrNoContainerRuntime
}
