package vzhelper

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSpecRoundTrip(t *testing.T) {
	dir := t.TempDir()

	disk := filepath.Join(dir, "disk.img")
	aux := filepath.Join(dir, "aux.img")
	for _, p := range []string{disk, aux} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	in := &MachineSpec{
		Name:              "mac",
		CPUs:              4,
		MemoryBytes:       8 << 30,
		MAC:               "52:54:00:12:34:56",
		Disks:             []DiskSpec{{Path: disk}},
		AuxiliaryStorage:  aux,
		HardwareModel:     []byte{0x01, 0x02},
		MachineIdentifier: []byte{0x03, 0x04},
		Vsock:             true,
	}
	if err := WriteSpec(dir, in); err != nil {
		t.Fatalf("WriteSpec: %v", err)
	}

	out, err := LoadSpec(dir)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if out.Name != in.Name || out.CPUs != in.CPUs || out.MemoryBytes != in.MemoryBytes || out.MAC != in.MAC {
		t.Errorf("round-trip mismatch: got %+v", out)
	}
	if !bytes.Equal(out.HardwareModel, in.HardwareModel) || !bytes.Equal(out.MachineIdentifier, in.MachineIdentifier) {
		t.Errorf("blob round-trip mismatch: got %+v", out)
	}
	if !out.Vsock {
		t.Error("Vsock did not survive the round-trip")
	}
	// Zero display must default, not stay zero (a macOS guest without a
	// display device hangs in the boot loader).
	if out.Display != DefaultDisplay {
		t.Errorf("Display = %+v, want default %+v", out.Display, DefaultDisplay)
	}
}

func TestSpecValidate(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.img")
	aux := filepath.Join(dir, "aux.img")
	for _, p := range []string{disk, aux} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	valid := MachineSpec{
		Name: "m", CPUs: 2, MemoryBytes: 1 << 30, MAC: "52:54:00:00:00:01",
		Disks: []DiskSpec{{Path: disk}}, AuxiliaryStorage: aux,
		HardwareModel: []byte{1}, MachineIdentifier: []byte{1},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*MachineSpec)
	}{
		{"zero cpus", func(s *MachineSpec) { s.CPUs = 0 }},
		{"zero memory", func(s *MachineSpec) { s.MemoryBytes = 0 }},
		{"no disks", func(s *MachineSpec) { s.Disks = nil }},
		{"missing disk file", func(s *MachineSpec) { s.Disks = []DiskSpec{{Path: filepath.Join(dir, "nope.img")}} }},
		{"no aux", func(s *MachineSpec) { s.AuxiliaryStorage = "" }},
		{"missing aux file", func(s *MachineSpec) { s.AuxiliaryStorage = filepath.Join(dir, "nope-aux.img") }},
		{"no hardware model", func(s *MachineSpec) { s.HardwareModel = nil }},
		{"no machine identifier", func(s *MachineSpec) { s.MachineIdentifier = nil }},
		{"no mac", func(s *MachineSpec) { s.MAC = "" }},
		{"kernel on a macos guest", func(s *MachineSpec) { s.Kernel = disk }},
		{"cmdline on a macos guest", func(s *MachineSpec) { s.Cmdline = "console=hvc0" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := valid
			tt.mutate(&s)
			if err := s.Validate(); err == nil {
				t.Errorf("expected validation error")
			}
		})
	}
}

func TestSpecValidateLinux(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.raw")
	if err := os.WriteFile(disk, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	valid := MachineSpec{
		Name: "lin", Platform: PlatformLinux, CPUs: 2, MemoryBytes: 1 << 30,
		MAC:   "52:54:00:00:00:02",
		Disks: []DiskSpec{{Path: disk}},
		// The variable store deliberately does not exist: the helper creates
		// it on first boot, so Validate must not stat it.
		EFIVariableStore: filepath.Join(dir, EFIVariableStoreName),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid linux spec rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*MachineSpec)
	}{
		{"no efi variable store", func(s *MachineSpec) { s.EFIVariableStore = "" }},
		{"mac restore artifacts on a linux guest", func(s *MachineSpec) { s.HardwareModel = []byte{1} }},
		{"aux storage on a linux guest", func(s *MachineSpec) { s.AuxiliaryStorage = disk }},
		{"unknown platform", func(s *MachineSpec) { s.Platform = "windows" }},
		{"cmdline without kernel", func(s *MachineSpec) { s.Cmdline = "console=hvc0" }},
		{"initrd without kernel", func(s *MachineSpec) { s.Initrd = disk }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := valid
			tt.mutate(&s)
			if err := s.Validate(); err == nil {
				t.Errorf("expected validation error")
			}
		})
	}
}

// Direct-kernel boot (issue #129): a Linux guest may boot from an external
// kernel image instead of an EFI disk — the two are mutually exclusive, and
// the kernel/initrd must exist up front (VZLinuxBootLoader reads them from
// the host; there is no first-boot creation like the EFI variable store).
func TestSpecValidateLinuxDirectKernel(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.raw")
	kernel := filepath.Join(dir, "vmlinux")
	initrd := filepath.Join(dir, "initrd.img")
	for _, p := range []string{disk, kernel, initrd} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	valid := MachineSpec{
		Name: "lin", Platform: PlatformLinux, CPUs: 2, MemoryBytes: 1 << 30,
		MAC:     "52:54:00:00:00:04",
		Disks:   []DiskSpec{{Path: disk}},
		Kernel:  kernel,
		Cmdline: "console=hvc0 root=/dev/vda",
		Initrd:  initrd,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid direct-kernel spec rejected: %v", err)
	}

	t.Run("kernel without cmdline or initrd", func(t *testing.T) {
		s := valid
		s.Cmdline = ""
		s.Initrd = ""
		if err := s.Validate(); err != nil {
			t.Errorf("bare kernel spec rejected: %v", err)
		}
	})

	tests := []struct {
		name   string
		mutate func(*MachineSpec)
	}{
		{"kernel and efi variable store", func(s *MachineSpec) { s.EFIVariableStore = EFIVariableStorePath(dir) }},
		{"missing kernel file", func(s *MachineSpec) { s.Kernel = filepath.Join(dir, "nope-vmlinux") }},
		{"missing initrd file", func(s *MachineSpec) { s.Initrd = filepath.Join(dir, "nope-initrd") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := valid
			tt.mutate(&s)
			if err := s.Validate(); err == nil {
				t.Errorf("expected validation error")
			}
		})
	}
}

// The direct-kernel fields must survive the spec file round-trip — the helper
// reads them back from vz-machine.json.
func TestSpecRoundTripDirectKernel(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.raw")
	kernel := filepath.Join(dir, "vmlinux")
	initrd := filepath.Join(dir, "initrd.img")
	for _, p := range []string{disk, kernel, initrd} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	in := &MachineSpec{
		Name: "lin", Platform: PlatformLinux, CPUs: 2, MemoryBytes: 1 << 30,
		MAC:     "52:54:00:00:00:05",
		Disks:   []DiskSpec{{Path: disk}},
		Kernel:  kernel,
		Cmdline: "console=hvc0 root=/dev/vda",
		Initrd:  initrd,
	}
	if err := WriteSpec(dir, in); err != nil {
		t.Fatalf("WriteSpec: %v", err)
	}
	out, err := LoadSpec(dir)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if out.Kernel != in.Kernel || out.Cmdline != in.Cmdline || out.Initrd != in.Initrd {
		t.Errorf("direct-kernel fields did not survive the round-trip: %+v", out)
	}
}

func TestLoadSpecLinuxKeepsZeroDisplay(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.raw")
	if err := os.WriteFile(disk, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := &MachineSpec{
		Name: "lin", Platform: PlatformLinux, CPUs: 2, MemoryBytes: 1 << 30,
		MAC:              "52:54:00:00:00:03",
		Disks:            []DiskSpec{{Path: disk}},
		EFIVariableStore: EFIVariableStorePath(dir),
		SerialLog:        SerialLogPath(dir),
	}
	if err := WriteSpec(dir, in); err != nil {
		t.Fatalf("WriteSpec: %v", err)
	}
	out, err := LoadSpec(dir)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	// A Linux guest is headless: the macOS display default must not leak in
	// (the helper would otherwise try to attach a mac graphics device).
	if out.Display != (DisplaySpec{}) {
		t.Errorf("Display = %+v, want zero for a linux guest", out.Display)
	}
	if out.PlatformName() != PlatformLinux {
		t.Errorf("PlatformName = %q, want %q", out.PlatformName(), PlatformLinux)
	}
	if out.EFIVariableStore != in.EFIVariableStore || out.SerialLog != in.SerialLog {
		t.Errorf("linux fields did not survive the round-trip: %+v", out)
	}
}

func TestPlatformNameDefaultsToMacOS(t *testing.T) {
	s := MachineSpec{}
	if s.PlatformName() != PlatformMacOS {
		t.Errorf("PlatformName() = %q, want %q for the zero value", s.PlatformName(), PlatformMacOS)
	}
}

func TestVsockBridgePath(t *testing.T) {
	path := VsockBridgePath("/vms/mac", 2222)
	if path != "/vms/mac/vz-vsock-2222.sock" {
		t.Errorf("VsockBridgePath = %q", path)
	}
	// The glob is what the helper uses to sweep stale bridge sockets at
	// startup — it must keep matching what VsockBridgePath produces.
	ok, err := filepath.Match(VsockBridgeGlob, filepath.Base(path))
	if err != nil || !ok {
		t.Errorf("VsockBridgeGlob %q does not match %q (err %v)", VsockBridgeGlob, filepath.Base(path), err)
	}
}

func TestResultRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := WriteResult(dir, &Result{StopRequested: true}); err != nil {
		t.Fatalf("WriteResult: %v", err)
	}
	res, err := LoadResult(dir)
	if err != nil {
		t.Fatalf("LoadResult: %v", err)
	}
	if !res.StopRequested || res.Error != "" {
		t.Errorf("got %+v", res)
	}
}

func TestParseMemoryBytes(t *testing.T) {
	tests := []struct {
		in      string
		want    uint64
		wantErr bool
	}{
		{"16G", 16 << 30, false},
		{"16g", 16 << 30, false},
		{"4096M", 4096 << 20, false},
		{"4096", 4096 << 20, false}, // bare = MiB, QEMU -m semantics
		{"512k", 512 << 10, false},
		{"1T", 1 << 40, false},
		{"", 0, true},
		{"abc", 0, true},
		{"12GB", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseMemoryBytes(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseMemoryBytes(%q): expected error, got %d", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseMemoryBytes(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseMemoryBytes(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
