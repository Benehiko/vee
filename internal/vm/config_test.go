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
