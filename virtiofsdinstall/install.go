package virtiofsdinstall

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const (
	virtiofsdVersion = "v1.13.3"
	virtiofsdSrcURL  = "https://gitlab.com/virtio-fs/virtiofsd/-/archive/" + virtiofsdVersion + "/virtiofsd-" + virtiofsdVersion + ".tar.gz"
)

// EnsureVirtiofsd ensures that a virtiofsd binary exists at ~/.vee/bin/virtiofsd.
// If already present it returns immediately. Otherwise it compiles from source
// inside a container (vessel → nerdctl → docker, first found wins).
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

	fmt.Fprintf(os.Stderr, "virtiofsd not found — building %s from source (this takes a few minutes)…\n", virtiofsdVersion)

	buildTmpDir := filepath.Join(os.TempDir(), "virtiofsd-build")
	if err := os.MkdirAll(buildTmpDir, 0o755); err != nil {
		return "", fmt.Errorf("create build dir: %w", err)
	}

	// Compile inside a rust:alpine container.
	// Bind-mount: build tmp dir as /build (writable workspace), binDir as /out (output).
	buildScript := fmt.Sprintf(`set -e
apk add --no-cache curl tar musl-dev >/dev/null 2>&1
curl -fsSL '%s' | tar -xz --strip-components=1 -C /build
cd /build
cargo build --release
cp target/release/virtiofsd /out/virtiofsd
`, virtiofsdSrcURL)

	// Use short flags (-v, --rm) which are supported by vessel, nerdctl, and docker.
	// Use "--" before IMAGE to prevent vessel from parsing sh's -c as a vessel flag.
	args := []string{
		"run", "--rm",
		"-v", binDir + ":/out",
		"-v", buildTmpDir + ":/build",
		"--",
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

	fmt.Fprintf(os.Stderr, "virtiofsd built successfully: %s\n", dst)
	return dst, nil
}

// findContainerRuntime returns the path to the first available container runtime.
func findContainerRuntime() (string, error) {
	for _, name := range []string{"vessel", "nerdctl", "docker"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("none of vessel/nerdctl/docker found in PATH")
}
