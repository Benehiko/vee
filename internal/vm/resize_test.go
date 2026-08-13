package vm

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseDiskSize(t *testing.T) {
	tests := []struct {
		spec     string
		relative bool
		bytes    int64
		wantErr  bool
	}{
		{spec: "60G", bytes: 60 << 30},
		{spec: "512M", bytes: 512 << 20},
		{spec: "1T", bytes: 1 << 40},
		{spec: "64k", bytes: 64 << 10},
		{spec: "1048576", bytes: 1 << 20},
		{spec: "+40G", relative: true, bytes: 40 << 30},
		{spec: " 20G ", bytes: 20 << 30},
		{spec: "", wantErr: true},
		{spec: "+", wantErr: true},
		{spec: "G", wantErr: true},
		{spec: "-5G", wantErr: true},
		{spec: "0G", wantErr: true},
		{spec: "1.5G", wantErr: true},
		{spec: "10GB", wantErr: true},
		{spec: "999999999999T", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			relative, bytes, err := ParseDiskSize(tt.spec)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseDiskSize(%q) = %v, %d; want error", tt.spec, relative, bytes)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDiskSize(%q): %v", tt.spec, err)
			}
			if relative != tt.relative || bytes != tt.bytes {
				t.Errorf("ParseDiskSize(%q) = %v, %d; want %v, %d", tt.spec, relative, bytes, tt.relative, tt.bytes)
			}
		})
	}
}

func TestHumanDiskSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{60 << 30, "60G"},
		{1 << 40, "1T"},
		{1536 << 20, "1536M"}, // 1.5G does not divide evenly by G
		{512, "512"},
		{20 << 30, "20G"},
	}
	for _, tt := range tests {
		if got := humanDiskSize(tt.bytes); got != tt.want {
			t.Errorf("humanDiskSize(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestFindResizableBootDisk(t *testing.T) {
	tests := []struct {
		name string
		cfg  *VMConfig
		want int
	}{
		{
			name: "managed qcow2 disk",
			cfg: &VMConfig{Disks: []DiskConfig{
				{Media: "disk", Format: "qcow2"},
			}},
			want: 0,
		},
		{
			name: "vz raw boot disk",
			cfg: &VMConfig{Disks: []DiskConfig{
				{Media: "cdrom", InstallISO: true},
				{Media: "disk", Format: "raw"},
			}},
			want: 1,
		},
		{
			name: "passthrough and readonly disks are skipped",
			cfg: &VMConfig{Disks: []DiskConfig{
				{Media: "disk", Format: "raw", Passthrough: true},
				{Media: "disk", Format: "qcow2", Readonly: true},
				{Media: "disk", Format: "qcow2"},
			}},
			want: 2,
		},
		{
			name: "no resizable disk",
			cfg: &VMConfig{Disks: []DiskConfig{
				{Media: "disk", Format: "raw", Passthrough: true},
			}},
			want: -1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := findResizableBootDisk(tt.cfg); got != tt.want {
				t.Errorf("findResizableBootDisk() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestResizeBootDisk exercises the full grow path against a real qcow2 image:
// image grows, config records the new size, and the managed file is renamed
// to embed it.
func TestResizeBootDisk(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not installed")
	}

	m := newTestManager(t)
	const name = "resizeme"

	vmDir := m.vmDir(name)
	img := filepath.Join(vmDir, "disk-resizeme-1G.qcow2")
	mustCreateQcow2(t, img, "1G")

	cfg := &VMConfig{
		Name: name,
		Disks: []DiskConfig{
			{Size: "1G", Format: "qcow2", Media: "disk", Interface: "virtio"},
		},
	}
	if err := m.SaveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	res, err := m.ResizeBootDisk(t.Context(), name, "2G")
	if err != nil {
		t.Fatalf("ResizeBootDisk: %v", err)
	}
	if res.OldSize != "1G" || res.NewSize != "2G" {
		t.Errorf("sizes = %s → %s, want 1G → 2G", res.OldSize, res.NewSize)
	}
	want := filepath.Join(vmDir, "disk-resizeme-2G.qcow2")
	if res.Path != want {
		t.Errorf("Path = %q, want %q (renamed to embed the new size)", res.Path, want)
	}

	got, err := m.LoadConfig(name)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got.Disks[0].Size != "2G" {
		t.Errorf("persisted Size = %q, want 2G", got.Disks[0].Size)
	}

	// Shrinking is refused.
	if _, err := m.ResizeBootDisk(t.Context(), name, "1G"); err == nil {
		t.Error("shrinking succeeded, want refusal")
	}
	// Same size is refused.
	if _, err := m.ResizeBootDisk(t.Context(), name, "2G"); err == nil {
		t.Error("no-op resize succeeded, want refusal")
	}

	// Relative grow on top of the previous resize.
	res, err = m.ResizeBootDisk(t.Context(), name, "+1G")
	if err != nil {
		t.Fatalf("relative ResizeBootDisk: %v", err)
	}
	if res.NewSize != "3G" {
		t.Errorf("NewSize = %q, want 3G", res.NewSize)
	}
}

func mustCreateQcow2(t *testing.T, path, size string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	out, err := exec.CommandContext(t.Context(), "qemu-img", "create", "-f", "qcow2", path, size).CombinedOutput() //nolint:gosec // test-owned temp path and size
	if err != nil {
		t.Fatalf("qemu-img create: %v: %s", err, out)
	}
}
