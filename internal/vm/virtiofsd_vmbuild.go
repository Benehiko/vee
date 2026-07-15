package vm

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/sshkeys"
	"github.com/Benehiko/vee/internal/virtiofsdinstall"
)

// virtiofsdBuildVMName is the fixed name of the transient VM used to compile
// virtiofsd when no host container runtime is available. It is created and
// destroyed within a single buildVirtiofsdInVM call.
const virtiofsdBuildVMName = "vee-virtiofsd-build"

// buildVirtiofsdInVM compiles virtiofsd inside a throwaway Ubuntu cloud-image VM
// and installs the resulting binary at <home>/.vee/bin/virtiofsd. It is the
// fallback used when EnsureVirtiofsd reports ErrNoContainerRuntime (no
// nerdctl/docker on the host).
//
// Ubuntu (glibc) is used deliberately: a musl-built virtiofsd 1.13.x segfaults at
// startup on a glibc host, which is why the container path uses rust:bookworm and
// why an Alpine build VM would not work here.
//
// The VM is always torn down before returning, including on error or ctx-cancel.
func (m *Manager) buildVirtiofsdInVM(ctx context.Context, home string) (string, error) {
	binDir := filepath.Join(home, ".vee", "bin")
	dst := filepath.Join(binDir, "virtiofsd")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		return "", fmt.Errorf("create bin dir: %w", err)
	}

	m.provider.Logger().Warn("no container runtime found — building virtiofsd inside a temporary VM (this is slow: boots a VM, runs apt + cargo)",
		zap.String("version", virtiofsdinstall.Version()))

	pubKey, privKeyPath, err := sshkeys.EnsureVeeKeyPair(home)
	if err != nil {
		return "", fmt.Errorf("ensure vee ssh key: %w", err)
	}
	privKeyPEM, err := readVeePrivateKey(privKeyPath)
	if err != nil {
		return "", err
	}

	cfg, err := m.newVirtiofsdBuildVMConfig(ctx, pubKey)
	if err != nil {
		return "", err
	}

	// Best-effort clean of any leftover VM from a previous interrupted run so
	// Create doesn't collide.
	_ = m.Stop(ctx, cfg.Name)
	_ = m.Delete(cfg.Name)

	if err := m.Create(ctx, cfg); err != nil {
		return "", fmt.Errorf("create build VM: %w", err)
	}
	// Teardown is best-effort and must run regardless of outcome. Use a detached
	// context so a cancelled ctx still lets us stop + delete the VM.
	defer func() {
		teardownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
		defer cancel()
		if serr := m.Stop(teardownCtx, cfg.Name); serr != nil {
			m.provider.Logger().Warn("failed to stop virtiofsd build VM", zap.Error(serr))
		}
		if derr := m.Delete(cfg.Name); derr != nil {
			m.provider.Logger().Warn("failed to delete virtiofsd build VM", zap.Error(derr))
		}
	}()

	if err := m.Start(ctx, cfg.Name, false); err != nil {
		return "", fmt.Errorf("start build VM: %w", err)
	}
	if err := m.WaitReady(ctx, cfg.Name, 5*time.Minute); err != nil {
		return "", fmt.Errorf("build VM did not become ready: %w", err)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.SSHPort)
	user := images.DefaultUser(images.DistroUbuntu)
	client, err := dialSSH(ctx, addr, user, privKeyPEM, 3*time.Minute)
	if err != nil {
		return "", fmt.Errorf("connect to build VM: %w", err)
	}
	defer func() { _ = client.Close() }()

	if err := runVirtiofsdBuild(ctx, client); err != nil {
		return "", err
	}

	// Pull the freshly built binary to a temp file, then atomically place it.
	tmp := dst + ".tmp"
	//nolint:gosec // G304: tmp is an internally constructed path under ~/.vee/bin.
	out, err := os.Create(tmp)
	if err != nil {
		return "", fmt.Errorf("create temp binary: %w", err)
	}
	if ferr := client.FetchFile(ctx, "/home/"+user+"/virtiofsd", out); ferr != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("fetch built virtiofsd: %w", ferr)
	}
	if cerr := out.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("close temp binary: %w", cerr)
	}

	// virtiofsd is an executable; it needs the exec bit.
	if err := os.Chmod(tmp, 0o750); err != nil { //nolint:gosec // G302: executable binary requires the exec bit; 0o750 is the tightest workable mode.
		_ = os.Remove(tmp)
		return "", fmt.Errorf("chmod virtiofsd: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("install virtiofsd: %w", err)
	}

	m.provider.Logger().Info("virtiofsd built in VM", zap.String("path", dst))
	fmt.Fprintf(os.Stderr, "virtiofsd built in VM: %s\n", dst)
	return dst, nil
}

// runVirtiofsdBuild installs the build dependencies inside the guest, downloads
// the pinned source archive, verifies its sha256, compiles it, and drops the
// binary in the default user's home directory for FetchFile to pull.
func runVirtiofsdBuild(ctx context.Context, client *sshExecClient) error {
	// The sha256 is verified in-guest via `sha256sum -c` against the same pinned
	// value the host container path uses. cargo needs network access to fetch the
	// crates registry; the guest has user-mode NAT.
	//
	// Rust comes from rustup, NOT apt: virtiofsd 1.13.x ships a Cargo.lock v4 that
	// requires Rust >= 1.78, but Ubuntu 24.04's apt `cargo` is too old and fails
	// with "lock file version 4 requires -Znext-lockfile-bump". rustup installs a
	// current stable toolchain. libseccomp/libcap-ng are link-time deps.
	//
	// DEBIAN_FRONTEND=noninteractive + apt -o quiet flags suppress the debconf
	// "no controlling tty" noise seen over a non-PTY SSH session.
	script := fmt.Sprintf(`set -e
export DEBIAN_FRONTEND=noninteractive
sudo -E apt-get update -qq
sudo -E apt-get install -y -qq -o Dpkg::Use-Pty=0 build-essential libseccomp-dev libcap-ng-dev pkg-config curl
curl -fSL %q -o /tmp/virtiofsd.tar.gz
echo %q | sha256sum -c -
rm -rf /tmp/virtiofsd-build
mkdir -p /tmp/virtiofsd-build
tar -xz --strip-components=1 -f /tmp/virtiofsd.tar.gz -C /tmp/virtiofsd-build
curl -fsSL https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain stable
. "$HOME/.cargo/env"
cd /tmp/virtiofsd-build
cargo build --release
cp target/release/virtiofsd "$HOME/virtiofsd"
`, virtiofsdinstall.SourceURL(), virtiofsdinstall.SourceSHA256()+"  /tmp/virtiofsd.tar.gz")

	// cargo build can take several minutes; bound it generously.
	buildCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	if _, stderr, err := client.Run(buildCtx, script); err != nil {
		return fmt.Errorf("in-VM virtiofsd build failed: %w (stderr: %s)", err, string(stderr))
	}
	return nil
}

// newVirtiofsdBuildVMConfig builds a minimal Ubuntu cloud-image VMConfig for the
// throwaway build VM. It mirrors the "server" template shape but is constructed
// inline because the vm package cannot import internal/templates (that package
// imports vm).
func (m *Manager) newVirtiofsdBuildVMConfig(ctx context.Context, pubKey string) (*VMConfig, error) {
	conf := m.provider.Config()

	img, err := images.NewImage(m.provider, images.DistroUbuntu, "latest")
	if err != nil {
		return nil, fmt.Errorf("build-VM image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("build-VM image download: %w", err)
	}

	name := virtiofsdBuildVMName
	vmDir := filepath.Join(conf.StoragePath, name)

	cpuModel := conf.DefaultCPUModel
	if cpuModel == "" {
		cpuModel = "host"
	}

	cfg := &VMConfig{
		Name:     name,
		Template: "server",
		Memory:   "4G",
		CPUs:     4,
		Sockets:  1,
		Cores:    4,
		Threads:  1,
		CPUModel: cpuModel,
		NIC: NICConfig{
			Mode:  "user",
			Model: "virtio-net-pci",
		},
		GPU:      GPUConfig{Mode: GPUNone},
		Headless: true,
		SSHPort:  deterministicSSHPortForBuild(name),
		// Cloud images use legacy BIOS boot; UEFI is not needed here.
		UEFI: UEFIConfig{Enabled: false},
		Disks: []DiskConfig{
			{
				Path:        filepath.Join(vmDir, "storage", "disk-os.img"),
				Size:        conf.DefaultDiskSize,
				Format:      "qcow2",
				Interface:   "virtio",
				Media:       "disk",
				Cache:       "writeback",
				BackingFile: img.AbsolutePath(),
			},
		},
		CloudInit: &CloudInitConfig{
			Hostname:    name,
			User:        images.DefaultUser(images.DistroUbuntu),
			DefaultUser: images.DefaultUser(images.DistroUbuntu),
			SSHKeys:     []string{pubKey},
		},
		CreatedAt: time.Now(),
	}
	return cfg, nil
}

// deterministicSSHPortForBuild maps the build VM name to a stable host port in
// [2300, 2399], kept out of the [2200, 2299] range the templates package uses so
// the throwaway build VM never collides with a user's server/devbox VM.
func deterministicSSHPortForBuild(name string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return 2300 + int(h.Sum32()%100)
}
