package vm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindManagedBootDisk(t *testing.T) {
	tests := []struct {
		name string
		cfg  *VMConfig
		want int
	}{
		{
			name: "single managed qcow2 disk",
			cfg: &VMConfig{Disks: []DiskConfig{
				{Media: "disk", Format: "qcow2"},
			}},
			want: 0,
		},
		{
			name: "cidata cdrom precedes boot disk",
			cfg: &VMConfig{Disks: []DiskConfig{
				{Media: "cdrom", Format: "qcow2", InstallISO: true},
				{Media: "disk", Format: "qcow2"},
			}},
			want: 1,
		},
		{
			name: "passthrough disk is skipped",
			cfg: &VMConfig{Disks: []DiskConfig{
				{Media: "disk", Format: "raw", Passthrough: true},
				{Media: "disk", Format: "qcow2"},
			}},
			want: 1,
		},
		{
			name: "first managed qcow2 wins when several present",
			cfg: &VMConfig{Disks: []DiskConfig{
				{Media: "disk", Format: "qcow2", Size: "40G"},
				{Media: "disk", Format: "qcow2", Size: "80G"},
			}},
			want: 0,
		},
		{
			name: "no managed disk (raw --boot-disk only)",
			cfg: &VMConfig{Disks: []DiskConfig{
				{Media: "disk", Format: "raw", Passthrough: true, BootIndex: 1},
			}},
			want: -1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := findManagedBootDisk(tt.cfg); got != tt.want {
				t.Errorf("findManagedBootDisk() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestManagedBootDiskAbsPath(t *testing.T) {
	tests := []struct {
		name string
		vm   string
		disk DiskConfig
		want string
	}{
		{
			name: "empty path resolves to generated name",
			vm:   "linux",
			disk: DiskConfig{Size: "40G", Format: "qcow2"},
			want: "disk-linux-40G.qcow2",
		},
		{
			name: "directory path gets generated name joined",
			vm:   "linux",
			disk: DiskConfig{Path: "/mnt/nvme/vms", Size: "40G", Format: "qcow2"},
			want: "/mnt/nvme/vms/disk-linux-40G.qcow2",
		},
		{
			name: "explicit file path used verbatim",
			vm:   "linux",
			disk: DiskConfig{Path: "/mnt/nvme/custom.qcow2", Size: "40G", Format: "qcow2"},
			want: "/mnt/nvme/custom.qcow2",
		},
		{
			name: "default format when unset",
			vm:   "linux",
			disk: DiskConfig{Path: "/data", Size: "40G"},
			want: "/data/disk-linux-40G.qcow2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := managedBootDiskAbsPath(tt.vm, tt.disk); got != tt.want {
				t.Errorf("managedBootDiskAbsPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMoveFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.qcow2")
	dst := filepath.Join(dir, "sub", "dst.qcow2")

	if err := os.WriteFile(src, []byte("disk-bytes"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}

	if err := moveFile(src, dst); err != nil {
		t.Fatalf("moveFile: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still exists after move: %v", err)
	}
	got, err := os.ReadFile(dst) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != "disk-bytes" {
		t.Errorf("dst content = %q, want %q", got, "disk-bytes")
	}
}
