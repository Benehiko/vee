package templates

import "testing"

// TestTruenasSerialFromPath covers the disk-by-id → QEMU serial derivation.
// ZFS identifies physical drives by these serials across reboots, so they must
// be stable, prefix-stripped, and within QEMU's 20-character limit.
func TestTruenasSerialFromPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{
			name: "ata prefix stripped",
			path: "/dev/disk/by-id/ata-ST22000NM000C",
			want: "ST22000NM000C",
		},
		{
			name: "nvme prefix stripped",
			path: "/dev/disk/by-id/nvme-Samsung_SSD_980",
			want: "Samsung_SSD_980",
		},
		{
			name: "partition suffix removed",
			path: "/dev/disk/by-id/ata-ST22000NM000C-part1",
			want: "ST22000NM000C",
		},
		{
			name: "truncated to QEMU's 20-char serial limit",
			path: "/dev/disk/by-id/ata-ST22000NM000C-3WC103_ZXA0S3H6",
			want: "ST22000NM000C-3WC103",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truenasSerialFromPath(tc.path)
			if got != tc.want {
				t.Errorf("truenasSerialFromPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
			if len(got) > 20 {
				t.Errorf("serial %q exceeds QEMU's 20-char limit", got)
			}
		})
	}
}

// TestParseDataDisk covers the "path" / "path:serial" data-disk spelling.
func TestParseDataDisk(t *testing.T) {
	if dd := ParseDataDisk("/dev/disk/by-id/ata-DISK"); dd.Path != "/dev/disk/by-id/ata-DISK" || dd.Serial != "" {
		t.Errorf("bare path parsed as %+v", dd)
	}
	if dd := ParseDataDisk("/dev/disk/by-id/ata-DISK:EXOS-A"); dd.Path != "/dev/disk/by-id/ata-DISK" || dd.Serial != "EXOS-A" {
		t.Errorf("path:serial parsed as %+v", dd)
	}
}
