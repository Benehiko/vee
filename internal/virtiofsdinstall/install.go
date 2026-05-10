package virtiofsdinstall

import (
	"context"
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
	virtiofsdVersion = "v1.13.3"
	virtiofsdSrcURL  = "https://gitlab.com/virtio-fs/virtiofsd/-/archive/" + virtiofsdVersion + "/virtiofsd-" + virtiofsdVersion + ".tar.gz"
)

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

	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("create bin dir: %w", err)
	}

	containerRuntime, err := findContainerRuntime()
	if err != nil {
		return "", fmt.Errorf("no container runtime found (vessel/nerdctl/docker required to build virtiofsd): %w", err)
	}

	fmt.Fprintf(os.Stderr, "virtiofsd not found — downloading and building %s (this takes a few minutes)…\n", virtiofsdVersion)

	// Download tarball on the host (has network access).
	buildDir := filepath.Join(os.TempDir(), "virtiofsd-build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return "", fmt.Errorf("create build dir: %w", err)
	}
	tarPath := filepath.Join(buildDir, "virtiofsd.tar.gz")

	if err := downloadFile(tarPath, virtiofsdSrcURL); err != nil {
		return "", fmt.Errorf("download virtiofsd source: %w", err)
	}

	// Build inside container — source tarball and output dir are bind-mounted.
	// Cargo needs network access to fetch the crates registry on first build.
	// libseccomp and libcap-ng are required link dependencies of virtiofsd.
	buildScript := `set -e
apk add --no-cache musl-dev libseccomp-dev libcap-ng-dev
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
		"rust:alpine",
		"sh", "-c", buildScript,
	}

	cmd := exec.CommandContext(context.Background(), containerRuntime, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("virtiofsd build failed: %w", err)
	}

	if err := os.Chmod(dst, 0o755); err != nil {
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
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(f, resp.Body)
	return err
}

// findContainerRuntime returns the path to the first available container runtime
// that supports network access inside containers (needed for cargo to fetch crates).
// vessel is excluded because its containers lack DNS resolution.
func findContainerRuntime() (string, error) {
	for _, name := range []string{"nerdctl", "docker"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("none of nerdctl/docker found in PATH (vessel not supported: no DNS in containers)")
}
