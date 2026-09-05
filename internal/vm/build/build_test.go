package build

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

// mustApplyOverrides runs applyOverrides and fails the test on error, keeping
// the call sites of tests that are not exercising disk validation terse.
func mustApplyOverrides(t *testing.T, cfg *vm.VMConfig, opts Opts, prov provider.Provider) {
	t.Helper()
	if err := applyOverrides(context.Background(), cfg, opts, prov); err != nil {
		t.Fatalf("applyOverrides: %v", err)
	}
}

// osDisk is a template's typical primary boot disk: a writable qcow2 placed
// under <storage_path>/<name>/storage by the template.
func osDisk(vmDir string) vm.DiskConfig {
	return vm.DiskConfig{
		Path:      filepath.Join(vmDir, "storage", "disk-os.qcow2"),
		Size:      "20G",
		Format:    "qcow2",
		Interface: "virtio",
		Media:     "disk",
	}
}

// applyOverrides does not dereference the provider, so a nil is safe here.
func TestApplyOverridesBootDiskPathRetargetsOSDisk(t *testing.T) {
	vmDir := filepath.Join("/home/user/.vee/vms", "t1")
	cfg := &vm.VMConfig{
		Name:  "t1",
		Disks: []vm.DiskConfig{osDisk(vmDir)},
	}

	mustApplyOverrides(t, cfg, Opts{Name: "t1", BootDiskPath: "/mnt/nvme"}, nil)

	if got := cfg.Disks[0].Path; got != "/mnt/nvme" {
		t.Errorf("boot disk Path: got %q, want %q", got, "/mnt/nvme")
	}
}

// The override must leave passthrough/data disks and non-disk media alone, only
// retargeting the first managed qcow2 OS disk.
func TestApplyOverridesBootDiskPathLeavesOtherDisksAlone(t *testing.T) {
	vmDir := filepath.Join("/home/user/.vee/vms", "t2")
	cdrom := vm.DiskConfig{Path: "/some/installer.iso", Format: "raw", Media: "cdrom"}
	data := vm.DiskConfig{Path: "/dev/disk/by-id/ata-DATA", Format: "raw", Media: "disk", Passthrough: true}
	cfg := &vm.VMConfig{
		Name:  "t2",
		Disks: []vm.DiskConfig{cdrom, osDisk(vmDir), data},
	}

	mustApplyOverrides(t, cfg, Opts{Name: "t2", BootDiskPath: "/mnt/nvme"}, nil)

	if cfg.Disks[0].Path != "/some/installer.iso" {
		t.Errorf("cdrom Path changed: %q", cfg.Disks[0].Path)
	}
	if cfg.Disks[1].Path != "/mnt/nvme" {
		t.Errorf("os disk Path: got %q, want /mnt/nvme", cfg.Disks[1].Path)
	}
	if cfg.Disks[2].Path != "/dev/disk/by-id/ata-DATA" {
		t.Errorf("passthrough data disk Path changed: %q", cfg.Disks[2].Path)
	}
}

// Nested plumbs through to the config, and its absence leaves the template
// value untouched (so a template could one day default it on).
func TestApplyOverridesNested(t *testing.T) {
	cfg := &vm.VMConfig{Name: "t4", Disks: []vm.DiskConfig{osDisk("/home/user/.vee/vms/t4")}}
	mustApplyOverrides(t, cfg, Opts{Name: "t4", Nested: true}, nil)
	if !cfg.Nested {
		t.Error("Opts.Nested=true did not set cfg.Nested")
	}

	cfg = &vm.VMConfig{Name: "t5", Disks: []vm.DiskConfig{osDisk("/home/user/.vee/vms/t5")}}
	mustApplyOverrides(t, cfg, Opts{Name: "t5"}, nil)
	if cfg.Nested {
		t.Error("cfg.Nested set without Opts.Nested")
	}
}

// Without the flag the template's default location is preserved.
func TestApplyOverridesNoBootDiskPathKeepsDefault(t *testing.T) {
	vmDir := filepath.Join("/home/user/.vee/vms", "t3")
	want := filepath.Join(vmDir, "storage", "disk-os.qcow2")
	cfg := &vm.VMConfig{
		Name:  "t3",
		Disks: []vm.DiskConfig{osDisk(vmDir)},
	}

	mustApplyOverrides(t, cfg, Opts{Name: "t3"}, nil)

	if got := cfg.Disks[0].Path; got != want {
		t.Errorf("boot disk Path: got %q, want %q", got, want)
	}
}

// diskProvider is the slice of provider.Provider the --disk override uses: it
// needs StoragePath to place the extra disk inside the VM's own directory.
type diskProvider struct{ cfg *provider.Config }

func (p *diskProvider) Config() *provider.Config { return p.cfg }
func (p *diskProvider) Logger() *zap.Logger      { return zap.NewNop() }
func (p *diskProvider) DB() *sql.DB              { return nil }

func newDiskProvider(storagePath string) provider.Provider {
	return &diskProvider{cfg: &provider.Config{StoragePath: storagePath}}
}

// TestApplyOverridesExtraDiskIsAppended covers the first half of issue #101:
// the extra disk used to be prepended, which demoted the cloud-image OS disk
// to slot 1. The firmware then tried the empty disk first, found no
// bootloader, and halted — a VM that reports "running" but never boots.
func TestApplyOverridesExtraDiskIsAppended(t *testing.T) {
	storage := "/home/user/.vee/vms"
	vmDir := filepath.Join(storage, "t6")
	cfg := &vm.VMConfig{
		Name:  "t6",
		Disks: []vm.DiskConfig{osDisk(vmDir)},
	}

	mustApplyOverrides(t, cfg, Opts{Name: "t6", Disk: "60G"}, newDiskProvider(storage))

	if len(cfg.Disks) != 2 {
		t.Fatalf("got %d disks, want 2: %+v", len(cfg.Disks), cfg.Disks)
	}
	if got, want := cfg.Disks[0].Path, filepath.Join(vmDir, "storage", "disk-os.qcow2"); got != want {
		t.Errorf("disk0 is %q, want the OS disk %q — the guest boots the first disk", got, want)
	}
	if cfg.Disks[1].Size != "60G" {
		t.Errorf("disk1 Size = %q, want the extra disk at 60G", cfg.Disks[1].Size)
	}
}

// TestApplyOverridesExtraDiskPathIsAbsolute covers the second half of #101:
// the entry had no Path, so qemu-img created the qcow2 under a bare relative
// name in whatever directory the user ran vee from.
func TestApplyOverridesExtraDiskPathIsAbsolute(t *testing.T) {
	storage := "/home/user/.vee/vms"
	cfg := &vm.VMConfig{
		Name:  "t7",
		Disks: []vm.DiskConfig{osDisk(filepath.Join(storage, "t7"))},
	}

	mustApplyOverrides(t, cfg, Opts{Name: "t7", Disk: "60G"}, newDiskProvider(storage))

	extra := cfg.Disks[len(cfg.Disks)-1]
	if extra.Path == "" {
		t.Fatal("extra disk has no Path; the qcow2 lands in the caller's working directory")
	}
	if !filepath.IsAbs(extra.Path) {
		t.Errorf("extra disk Path %q is relative", extra.Path)
	}
	if want := filepath.Join(storage, "t7", "storage"); filepath.Dir(extra.Path) != want {
		t.Errorf("extra disk directory = %q, want %q", filepath.Dir(extra.Path), want)
	}
	// It must not collide with the OS disk that shares that directory.
	if extra.Path == cfg.Disks[0].Path {
		t.Errorf("extra disk shares the OS disk's path %q", extra.Path)
	}
}

// vz macOS guests consume Opts.Disk as the raw restore-disk size inside the
// template; the vz backend is raw-only, so a generic qcow2 here breaks start.
func TestApplyOverridesExtraDiskSkippedForVZMacOS(t *testing.T) {
	storage := "/home/user/.vee/vms"
	cfg := &vm.VMConfig{
		Name:    "t8",
		Backend: string(backend.VZ),
		MacOS:   &vm.MacOSConfig{},
		Disks:   []vm.DiskConfig{osDisk(filepath.Join(storage, "t8"))},
	}

	mustApplyOverrides(t, cfg, Opts{Name: "t8", Disk: "60G"}, newDiskProvider(storage))

	if len(cfg.Disks) != 1 {
		t.Errorf("vz macOS guest got %d disks, want the template's 1: %+v", len(cfg.Disks), cfg.Disks)
	}
}

// A vz Linux guest keeps the extra disk: Manager.Create materializes the
// qcow2 entry as a raw image at create time (issue #127).
func TestApplyOverridesExtraDiskKeptForVZLinux(t *testing.T) {
	storage := "/home/user/.vee/vms"
	cfg := &vm.VMConfig{
		Name:  "t9",
		Disks: []vm.DiskConfig{osDisk(filepath.Join(storage, "t9"))},
	}

	mustApplyOverrides(t, cfg, Opts{Name: "t9", Backend: string(backend.VZ), Disk: "60G"}, newDiskProvider(storage))

	if len(cfg.Disks) != 2 {
		t.Fatalf("vz Linux guest got %d disks, want 2: %+v", len(cfg.Disks), cfg.Disks)
	}
	if cfg.Disks[1].Size != "60G" {
		t.Errorf("extra disk Size = %q, want 60G", cfg.Disks[1].Size)
	}
}

// The --backend override lands in the config, and its absence keeps the
// template's backend (empty = QEMU for every non-macos template).
func TestApplyOverridesBackend(t *testing.T) {
	cfg := &vm.VMConfig{Name: "t10", Disks: []vm.DiskConfig{osDisk("/home/user/.vee/vms/t10")}}
	mustApplyOverrides(t, cfg, Opts{Name: "t10", Backend: string(backend.VZ)}, nil)
	if cfg.BackendName() != backend.VZ {
		t.Errorf("BackendName = %q, want %q", cfg.BackendName(), backend.VZ)
	}

	cfg = &vm.VMConfig{Name: "t11", Disks: []vm.DiskConfig{osDisk("/home/user/.vee/vms/t11")}}
	mustApplyOverrides(t, cfg, Opts{Name: "t11"}, nil)
	if cfg.BackendName() != backend.QEMU {
		t.Errorf("BackendName = %q, want the QEMU default", cfg.BackendName())
	}
}

// A --boot-disk naming an existing qcow2 must be adopted as an image file with
// its real format, not described as a raw passthrough device. Getting this wrong
// hands QEMU `format=raw,file=...qcow2`, so the guest firmware reads the qcow2
// container header where the partition table should be, finds no bootable
// filesystem, and drops to the EFI shell.
func TestApplyOverridesBootDiskQcow2Image(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "ubuntu.qcow2")
	writeQcow2(t, img)

	cfg := &vm.VMConfig{Name: "img1"}
	mustApplyOverrides(t, cfg, Opts{Name: "img1", BootDisk: img, NoAutoInstall: true}, nil)

	d := findDiskByPath(t, cfg, img)
	if d.Format != "qcow2" {
		t.Errorf("adopted qcow2 boot disk should have format qcow2, got %q", d.Format)
	}
	if d.Passthrough {
		t.Error("an image file must not be marked as block-device passthrough")
	}
	if !d.ImageFile {
		t.Error("an existing image file boot disk should be marked ImageFile")
	}
	if d.BootIndex != 1 {
		t.Errorf("boot disk should get bootindex 1, got %d", d.BootIndex)
	}
}

// A raw image file is still an image file, not a passthrough device: the format
// coincides but the ownership semantics (never created, never resized) differ.
func TestApplyOverridesBootDiskRawImage(t *testing.T) {
	requireQemuImg(t)
	dir := t.TempDir()
	img := filepath.Join(dir, "disk.img")
	if err := os.WriteFile(img, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &vm.VMConfig{Name: "img2"}
	mustApplyOverrides(t, cfg, Opts{Name: "img2", BootDisk: img, NoAutoInstall: true}, nil)

	d := findDiskByPath(t, cfg, img)
	if d.Format != "raw" {
		t.Errorf("raw image should have format raw, got %q", d.Format)
	}
	if !d.ImageFile || d.Passthrough {
		t.Errorf("raw image should be ImageFile and not Passthrough, got ImageFile=%t Passthrough=%t",
			d.ImageFile, d.Passthrough)
	}
}

// A path that is neither a block device nor a regular file must be rejected with
// a message naming the path, rather than silently producing a VM that cannot boot.
func TestApplyOverridesBootDiskRejectsNonDisk(t *testing.T) {
	t.Run("missing path", func(t *testing.T) {
		cfg := &vm.VMConfig{Name: "bad1"}
		err := applyOverrides(context.Background(), cfg,
			Opts{Name: "bad1", BootDisk: filepath.Join(t.TempDir(), "nope.qcow2")}, nil)
		if err == nil {
			t.Fatal("expected an error for a missing --boot-disk path")
		}
		if !strings.Contains(err.Error(), "nope.qcow2") {
			t.Errorf("error should name the offending path, got: %v", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &vm.VMConfig{Name: "bad2"}
		err := applyOverrides(context.Background(), cfg, Opts{Name: "bad2", BootDisk: dir}, nil)
		if err == nil {
			t.Fatal("expected an error for a directory --boot-disk path")
		}
	})
}

// --boot-disk-path relocates only the disk vee manages itself; it must not
// retarget an adopted image, whose path is the user's own file.
func TestApplyOverridesBootDiskPathLeavesAdoptedImage(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "ubuntu.qcow2")
	writeQcow2(t, img)

	cfg := &vm.VMConfig{Name: "img3"}
	mustApplyOverrides(t, cfg,
		Opts{Name: "img3", BootDisk: img, NoAutoInstall: true, BootDiskPath: "/mnt/nvme"}, nil)

	d := findDiskByPath(t, cfg, img)
	if d.Path != img {
		t.Errorf("adopted image path should be untouched by --boot-disk-path, got %q", d.Path)
	}
}

// requireQemuImg skips the test when qemu-img is unavailable. Disk classification
// probes the image format with it, so any test that feeds an image file through
// applyOverrides needs it present — CI runners do not all ship qemu-img.
func requireQemuImg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not available; format detection cannot be exercised")
	}
}

// writeQcow2 creates a real, minimal qcow2 file. The format is probed with
// qemu-img, so a valid header — not just the extension — is what matters.
func writeQcow2(t *testing.T, path string) {
	t.Helper()
	requireQemuImg(t)
	//nolint:gosec // test-local path
	if out, err := exec.CommandContext(context.Background(), "qemu-img", "create", "-f", "qcow2", path, "64M").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img create: %v: %s", err, out)
	}
}

func findDiskByPath(t *testing.T, cfg *vm.VMConfig, path string) vm.DiskConfig {
	t.Helper()
	for _, d := range cfg.Disks {
		if d.Path == path {
			return d
		}
	}
	t.Fatalf("no disk with path %q in config (%d disks)", path, len(cfg.Disks))
	return vm.DiskConfig{}
}

// The three virtio-GPU acceleration knobs are only read on the GPUVirtio path,
// so applying them to any other GPU mode must fail loudly rather than produce a
// VM that silently ignores them.
func TestApplyOverridesGPUAccelRequiresVirtio(t *testing.T) {
	venus := true
	cases := []struct {
		name string
		opts Opts
		want string
	}{
		{"gl backend", Opts{Name: "t1", GPUMode: "passthrough", GLBackend: "on"}, "--gpu-gl-backend"},
		{"venus", Opts{Name: "t1", GPUMode: "passthrough", Venus: &venus}, "--gpu-venus"},
		{"hostmem", Opts{Name: "t1", GPUMode: "none", HostMem: "8G"}, "--gpu-hostmem"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := applyOverrides(context.Background(), &vm.VMConfig{Name: "t1"}, c.opts, nil)
			if err == nil {
				t.Fatalf("applyOverrides(%+v): got nil error, want one mentioning %s", c.opts, c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %s", err, c.want)
			}
		})
	}
}

// host_mem only reaches the device string alongside venus, so asking for one
// without the other is a mistake worth reporting rather than dropping.
func TestApplyOverridesHostMemRequiresVenus(t *testing.T) {
	err := applyOverrides(context.Background(), &vm.VMConfig{Name: "t1"},
		Opts{Name: "t1", GPUMode: "virtio", HostMem: "8G"}, nil)
	if err == nil || !strings.Contains(err.Error(), "--gpu-venus") {
		t.Fatalf("applyOverrides: got %v, want an error pointing at --gpu-venus", err)
	}
}

func TestApplyOverridesGPUAccelApplied(t *testing.T) {
	venus := true
	cfg := &vm.VMConfig{Name: "t1"}
	mustApplyOverrides(t, cfg, Opts{
		Name:      "t1",
		GPUMode:   "virtio",
		GLBackend: "on",
		Venus:     &venus,
		HostMem:   "16G",
	}, nil)

	if cfg.GPU.Mode != vm.GPUVirtio || cfg.GPU.GLBackend != "on" || !cfg.GPU.Venus || cfg.GPU.HostMem != "16G" {
		t.Errorf("GPU config: got %+v, want virtio/on/venus/16G", cfg.GPU)
	}
}

// An unset host_mem is valid: the device builder fills in its own default, so
// --gpu-venus alone is a complete, working invocation.
func TestApplyOverridesVenusWithoutHostMemIsValid(t *testing.T) {
	venus := true
	cfg := &vm.VMConfig{Name: "t1"}
	mustApplyOverrides(t, cfg, Opts{Name: "t1", GPUMode: "virtio", Venus: &venus}, nil)

	if !cfg.GPU.Venus || cfg.GPU.HostMem != "" {
		t.Errorf("GPU config: got %+v, want venus enabled with an empty host_mem", cfg.GPU)
	}
}

func TestApplyOverridesRejectsUnknownGLBackend(t *testing.T) {
	err := applyOverrides(context.Background(), &vm.VMConfig{Name: "t1"},
		Opts{Name: "t1", GPUMode: "virtio", GLBackend: "metal"}, nil)
	if err == nil || !strings.Contains(err.Error(), "metal") {
		t.Fatalf("applyOverrides: got %v, want an error naming the invalid backend", err)
	}
}
