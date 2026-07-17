package build

import (
	"path/filepath"
	"testing"

	"github.com/Benehiko/vee/internal/vm"
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
