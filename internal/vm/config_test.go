package vm

import "testing"

func TestDiskConfigIsInstallISO(t *testing.T) {
	tests := []struct {
		name string
		disk DiskConfig
		want bool
	}{
		{
			name: "explicit install_iso flag",
			disk: DiskConfig{Media: "cdrom", InstallISO: true},
			want: true,
		},
		{
			name: "legacy cdrom without install_iso flag",
			disk: DiskConfig{Media: "cdrom"},
			want: true,
		},
		{
			name: "primary os disk",
			disk: DiskConfig{Media: "disk", Interface: "ahci"},
			want: false,
		},
		{
			name: "passthrough data disk",
			disk: DiskConfig{Media: "disk", Passthrough: true},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.disk.IsInstallISO(); got != tt.want {
				t.Errorf("IsInstallISO() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDiskConfigIsScratch(t *testing.T) {
	if (DiskConfig{Media: "disk", Scratch: true}).IsScratch() != true {
		t.Errorf("scratch disk should report IsScratch() = true")
	}
	if (DiskConfig{Media: "disk"}).IsScratch() != false {
		t.Errorf("non-scratch disk should report IsScratch() = false")
	}
	// A scratch disk is never treated as an install ISO (it is a real disk).
	if (DiskConfig{Media: "disk", Scratch: true}).IsInstallISO() != false {
		t.Errorf("scratch disk should not be an install ISO")
	}
}

func TestScratchDiskPath(t *testing.T) {
	tests := []struct {
		name string
		vm   string
		disk DiskConfig
		want string
	}{
		{
			name: "generated name for empty path",
			vm:   "wintest",
			disk: DiskConfig{Size: "8G", Format: "qcow2", Scratch: true},
			want: "disk-wintest-8G.qcow2",
		},
		{
			name: "default format when unset",
			vm:   "wintest",
			disk: DiskConfig{Size: "8G", Scratch: true},
			want: "disk-wintest-8G.qcow2",
		},
		{
			name: "explicit file path used verbatim",
			vm:   "wintest",
			disk: DiskConfig{Path: "/data/scratch.qcow2", Scratch: true},
			want: "/data/scratch.qcow2",
		},
		{
			name: "explicit directory gets generated name joined",
			vm:   "wintest",
			disk: DiskConfig{Path: "/data", Size: "8G", Format: "qcow2", Scratch: true},
			want: "/data/disk-wintest-8G.qcow2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scratchDiskPath(tt.vm, tt.disk); got != tt.want {
				t.Errorf("scratchDiskPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCheckDisksForDataSkipsLegacyCdrom guards the regression where a legacy
// TrueNAS config carried a media=cdrom installer disk with no install_iso flag.
// The data-check must skip it (a cdrom is never a data disk) rather than trying
// to stat/inspect a possibly-missing ISO path.
func TestCheckDisksForDataSkipsLegacyCdrom(t *testing.T) {
	cfg := &VMConfig{
		Disks: []DiskConfig{
			{Path: "/does/not/exist/installer.iso", Media: "cdrom"},
		},
	}
	warnings, err := CheckDisksForData(cfg)
	if err != nil {
		t.Fatalf("CheckDisksForData returned error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings for a cdrom disk, got %+v", warnings)
	}
}
