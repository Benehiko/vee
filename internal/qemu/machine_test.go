package qemu_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/Benehiko/vee/internal/platform"
	"github.com/Benehiko/vee/internal/qemu"
)

// argValue returns the value following the first occurrence of flag in args, or
// "" if the flag is absent or has no following value.
func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestMachineAcceleratorDefault(t *testing.T) {
	p := newTestProvider(t)
	m, err := qemu.NewEmptyMachine(p)
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}
	args := m.Args()

	if !slices.Contains(args, "-accel") {
		t.Fatalf("expected -accel flag in args: %v", args)
	}
	// The default accelerator is host-derived: hvf on macOS, kvm on Linux.
	want := platform.DefaultAccelerator()
	if got := argValue(args, "-accel"); got != want {
		t.Errorf("default accelerator: got %q, want %q", got, want)
	}
	// The legacy -enable-kvm shorthand must no longer be emitted.
	if slices.Contains(args, "-enable-kvm") {
		t.Errorf("-enable-kvm should no longer be emitted: %v", args)
	}
}

func TestMachineAcceleratorOverride(t *testing.T) {
	p := newTestProvider(t)
	m, err := qemu.NewEmptyMachine(p)
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}
	disk := qemu.NewDisk(p, m, qemu.WithCustomPath("/data/disk.qcow2"))
	built, err := m.BuildMachine(
		qemu.AddDisk(disk),
		qemu.WithAccelerator(qemu.AccelTCG),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}
	if got := argValue(built.Args(), "-accel"); got != string(qemu.AccelTCG) {
		t.Errorf("overridden accelerator: got %q, want %q", got, qemu.AccelTCG)
	}
}

func TestMachineAArch64VirtGIC(t *testing.T) {
	p := newTestProvider(t)
	m, err := qemu.NewEmptyMachine(p)
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}
	disk := qemu.NewDisk(p, m, qemu.WithCustomPath("/data/disk.qcow2"))
	built, err := m.BuildMachine(
		qemu.AddDisk(disk),
		qemu.WithArchitecture("aarch64"),
		qemu.WithMachineType("virt"),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}
	got := argValue(built.Args(), "-machine")
	if got != "virt,gic-version=max" {
		t.Errorf("aarch64 virt machine: got %q, want virt,gic-version=max", got)
	}
}

func TestMachineX86NoGIC(t *testing.T) {
	p := newTestProvider(t)
	m, err := qemu.NewEmptyMachine(p)
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}
	disk := qemu.NewDisk(p, m, qemu.WithCustomPath("/data/disk.qcow2"))
	built, err := m.BuildMachine(
		qemu.AddDisk(disk),
		qemu.WithArchitecture("x86_64"),
		qemu.WithMachineType("q35"),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}
	if got := argValue(built.Args(), "-machine"); got != "q35" {
		t.Errorf("x86_64 machine should not get gic-version: got %q", got)
	}
}

func TestMachineArchitectureDefault(t *testing.T) {
	p := newTestProvider(t)
	m, err := qemu.NewEmptyMachine(p)
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}
	// Architecture defaults to the host's native guest arch (aarch64 on Apple
	// Silicon). It is not surfaced directly in Args() yet (binary selection is
	// handled by the provider), so assert the machine builds and -accel/-machine
	// are present and ordered as expected.
	args := m.Args()
	if !slices.Contains(args, "-machine") {
		t.Fatalf("expected -machine flag: %v", args)
	}
	mi := slices.Index(args, "-machine")
	ai := slices.Index(args, "-accel")
	if ai != mi+2 {
		t.Errorf("-accel should immediately follow the -machine pair; args: %s", strings.Join(args, " "))
	}
}
