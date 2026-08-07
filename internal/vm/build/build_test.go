package build

import (
	"database/sql"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

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

	applyOverrides(cfg, Opts{Name: "t1", BootDiskPath: "/mnt/nvme"}, nil)

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

	applyOverrides(cfg, Opts{Name: "t2", BootDiskPath: "/mnt/nvme"}, nil)

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
	applyOverrides(cfg, Opts{Name: "t4", Nested: true}, nil)
	if !cfg.Nested {
		t.Error("Opts.Nested=true did not set cfg.Nested")
	}

	cfg = &vm.VMConfig{Name: "t5", Disks: []vm.DiskConfig{osDisk("/home/user/.vee/vms/t5")}}
	applyOverrides(cfg, Opts{Name: "t5"}, nil)
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

	applyOverrides(cfg, Opts{Name: "t3"}, nil)

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

	applyOverrides(cfg, Opts{Name: "t6", Disk: "60G"}, newDiskProvider(storage))

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

	applyOverrides(cfg, Opts{Name: "t7", Disk: "60G"}, newDiskProvider(storage))

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

// vz guests consume Opts.Disk as the raw restore-disk size inside the
// template; the vz backend is raw-only, so a generic qcow2 here breaks start.
func TestApplyOverridesExtraDiskSkippedForVZ(t *testing.T) {
	storage := "/home/user/.vee/vms"
	cfg := &vm.VMConfig{
		Name:    "t8",
		Backend: string(backend.VZ),
		Disks:   []vm.DiskConfig{osDisk(filepath.Join(storage, "t8"))},
	}

	applyOverrides(cfg, Opts{Name: "t8", Disk: "60G"}, newDiskProvider(storage))

	if len(cfg.Disks) != 1 {
		t.Errorf("vz guest got %d disks, want the template's 1: %+v", len(cfg.Disks), cfg.Disks)
	}
}
