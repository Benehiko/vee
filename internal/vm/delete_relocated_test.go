package vm

import (
	"path/filepath"
	"testing"
)

func TestRelocatedBootDisk(t *testing.T) {
	vmDir := "/home/u/.vee/vms/win"
	managed := func(path string) *VMConfig {
		return &VMConfig{Disks: []DiskConfig{
			{Path: "/iso/win.iso", Media: "cdrom", Format: "raw"},
			{Path: path, Size: "64G", Format: "qcow2", Media: "disk"},
		}}
	}
	cases := []struct {
		name string
		cfg  *VMConfig
		want string
	}{
		{"relocated directory", managed("/mnt/4TB/vee"), filepath.Join("/mnt/4TB/vee", "disk-win-64G.qcow2")},
		{"inside the VM dir", managed(filepath.Join(vmDir, "storage")), ""},
		{"empty path", managed(""), ""},
		{"explicit user file", managed("/mnt/4TB/my.qcow2"), ""},
		{"passthrough only", &VMConfig{Disks: []DiskConfig{{Path: "/dev/nvme0n1", Media: "disk", Format: "raw", Passthrough: true}}}, ""},
		{"adopted image", &VMConfig{Disks: []DiskConfig{{Path: "/mnt/4TB/vee", Size: "64G", Media: "disk", Format: "qcow2", ImageFile: true}}}, ""},
	}
	for _, c := range cases {
		if got := relocatedBootDisk("win", vmDir, c.cfg); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
