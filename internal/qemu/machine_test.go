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

func TestMachineAArch64VirtNested(t *testing.T) {
	p := newTestProvider(t)
	m, err := qemu.NewEmptyMachine(p)
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}
	disk := qemu.NewDisk(p, m, qemu.WithCustomPath("/data/disk.qcow2"))
	// Pin a non-HVF accelerator: the host-derived default would be hvf on a
	// macOS runner, where nested additionally appends kernel-irqchip=on
	// (covered by TestMachineNestedHVFKernelIrqchip).
	built, err := m.BuildMachine(
		qemu.AddDisk(disk),
		qemu.WithArchitecture("aarch64"),
		qemu.WithMachineType("virt"),
		qemu.WithAccelerator(qemu.AccelTCG),
		qemu.WithNested(true),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}
	got := argValue(built.Args(), "-machine")
	if got != "virt,gic-version=max,virtualization=on" {
		t.Errorf("nested aarch64 virt machine: got %q, want virt,gic-version=max,virtualization=on", got)
	}
}

func TestMachineNestedHVFKernelIrqchip(t *testing.T) {
	// The HVF nested path (QEMU 11.1+) requires Apple's in-kernel vGIC — EL2
	// with the userspace-GIC fallback is refused at start — so nested under
	// HVF must carry kernel-irqchip=on.
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
		qemu.WithAccelerator(qemu.AccelHVF),
		qemu.WithNested(true),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}
	got := argValue(built.Args(), "-machine")
	if got != "virt,gic-version=max,virtualization=on,kernel-irqchip=on" {
		t.Errorf("nested HVF machine: got %q, want virt,gic-version=max,virtualization=on,kernel-irqchip=on", got)
	}
}

func TestMachineNestedHVFBootMenuWorkaround(t *testing.T) {
	// edk2 hangs forever at EL2 under HVF waiting on a virtual-timer interrupt
	// Hypervisor.framework never delivers (Apple FB21649319); the upstream
	// series' documented workaround is -boot menu=on,splash-time=0.
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
		qemu.WithAccelerator(qemu.AccelHVF),
		qemu.WithNested(true),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}
	if got := argValue(built.Args(), "-boot"); got != "menu=on,splash-time=0" {
		t.Errorf("nested HVF boot workaround missing: got %q", got)
	}

	// With an explicit boot order the workaround merges rather than replaces.
	m2, _ := qemu.NewEmptyMachine(p)
	disk2 := qemu.NewDisk(p, m2, qemu.WithCustomPath("/data/disk.qcow2"))
	built2, err := m2.BuildMachine(
		qemu.AddDisk(disk2),
		qemu.WithArchitecture("aarch64"),
		qemu.WithMachineType("virt"),
		qemu.WithAccelerator(qemu.AccelHVF),
		qemu.WithNested(true),
		qemu.WithBootOrder("c"),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}
	if got := argValue(built2.Args(), "-boot"); got != "order=c,menu=on,splash-time=0" {
		t.Errorf("nested HVF boot order merge: got %q", got)
	}

	// Non-nested machines keep the quiet menu=off behaviour.
	m3, _ := qemu.NewEmptyMachine(p)
	disk3 := qemu.NewDisk(p, m3, qemu.WithCustomPath("/data/disk.qcow2"))
	built3, err := m3.BuildMachine(
		qemu.AddDisk(disk3),
		qemu.WithArchitecture("aarch64"),
		qemu.WithMachineType("virt"),
		qemu.WithAccelerator(qemu.AccelHVF),
		qemu.WithBootOrder("c"),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}
	if got := argValue(built3.Args(), "-boot"); got != "order=c,menu=off" {
		t.Errorf("non-nested boot order changed: got %q", got)
	}
}

func TestMachineNestedKVMNoKernelIrqchip(t *testing.T) {
	// KVM's in-kernel GIC is the default there; the explicit pairing is an
	// HVF-only need and must not leak onto other accelerators.
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
		qemu.WithAccelerator(qemu.AccelKVM),
		qemu.WithNested(true),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}
	if got := argValue(built.Args(), "-machine"); strings.Contains(got, "kernel-irqchip") {
		t.Errorf("kernel-irqchip must not be added under KVM: got %q", got)
	}
}

func TestMachineNestedHVFRespectsExplicitIrqchip(t *testing.T) {
	// An explicit kernel-irqchip value in machine_type wins, mirroring how
	// gic-version and virtualization are handled.
	p := newTestProvider(t)
	m, err := qemu.NewEmptyMachine(p)
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}
	disk := qemu.NewDisk(p, m, qemu.WithCustomPath("/data/disk.qcow2"))
	built, err := m.BuildMachine(
		qemu.AddDisk(disk),
		qemu.WithArchitecture("aarch64"),
		qemu.WithMachineType("virt,kernel-irqchip=off"),
		qemu.WithAccelerator(qemu.AccelHVF),
		qemu.WithNested(true),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}
	got := argValue(built.Args(), "-machine")
	if strings.Contains(got, "kernel-irqchip=on") {
		t.Errorf("explicit kernel-irqchip=off was overridden: got %q", got)
	}
}

func TestMachineNestedRespectsExplicitVirtualization(t *testing.T) {
	// An explicit virtualization= value in the machine type wins over the
	// nested option, mirroring how gic-version is handled.
	p := newTestProvider(t)
	m, err := qemu.NewEmptyMachine(p)
	if err != nil {
		t.Fatalf("NewEmptyMachine: %v", err)
	}
	disk := qemu.NewDisk(p, m, qemu.WithCustomPath("/data/disk.qcow2"))
	built, err := m.BuildMachine(
		qemu.AddDisk(disk),
		qemu.WithArchitecture("aarch64"),
		qemu.WithMachineType("virt,virtualization=off"),
		qemu.WithNested(true),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}
	got := argValue(built.Args(), "-machine")
	if strings.Contains(got, "virtualization=on") {
		t.Errorf("explicit virtualization=off was overridden: got %q", got)
	}
}

func TestMachineNestedIgnoredOffAArch64(t *testing.T) {
	// virtualization= is a property of the aarch64 virt board only; a hand-
	// edited config carrying nested on an x86_64 guest must not produce it.
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
		qemu.WithNested(true),
	)
	if err != nil {
		t.Fatalf("BuildMachine: %v", err)
	}
	if got := argValue(built.Args(), "-machine"); got != "q35" {
		t.Errorf("x86_64 machine should ignore nested: got %q", got)
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
