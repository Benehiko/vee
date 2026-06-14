package qemu_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/Benehiko/vee/internal/qemu"
)

func TestCPUArgsHostModel(t *testing.T) {
	p := newTestProvider(t)
	cpu := qemu.NewCPU(p, qemu.WithCPUModel(qemu.CPUHost))
	args := cpu.Args()
	joined := strings.Join(args, " ")

	if !slices.Contains(args, "-cpu") {
		t.Fatalf("missing -cpu flag: %v", args)
	}
	if !strings.Contains(joined, "host") {
		t.Errorf("expected 'host' model in args: %s", joined)
	}
}

func TestCPUArgsModelVariants(t *testing.T) {
	models := []qemu.CPUModel{
		qemu.CPUCascadeLakeServer,
		qemu.CPUCascadeLakeServerNoTSX,
		qemu.CPUSkylakeServer,
		qemu.CPUSkylakeServerIBRS,
		qemu.CPUBroadwell,
		qemu.CPUHaswell,
		qemu.CPUIvyBridge,
		qemu.CPUSandyBridge,
		qemu.CPUNehalem,
		qemu.CPUPenryn,
		qemu.CPUConroe,
	}

	for _, model := range models {
		p := newTestProvider(t)
		cpu := qemu.NewCPU(p, qemu.WithCPUModel(model))
		args := cpu.Args()
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, string(model)) {
			t.Errorf("model %q not found in args: %s", model, joined)
		}
	}
}

func TestCPUSMPArgs(t *testing.T) {
	p := newTestProvider(t)
	cpu := qemu.NewCPU(p, qemu.WithSMP(4, 1, 2, 2))
	args := cpu.Args()
	joined := strings.Join(args, " ")

	if !slices.Contains(args, "-smp") {
		t.Fatalf("missing -smp flag: %v", args)
	}
	if !strings.Contains(joined, "sockets=1") {
		t.Errorf("missing sockets=1 in: %s", joined)
	}
	if !strings.Contains(joined, "cores=2") {
		t.Errorf("missing cores=2 in: %s", joined)
	}
	if !strings.Contains(joined, "threads=2") {
		t.Errorf("missing threads=2 in: %s", joined)
	}
}

// TestCPUEmitsNoAccelerator verifies that acceleration is no longer selected at
// the CPU level — it moved to the machine level (-accel). The CPU builder must
// only contribute -cpu/-smp.
func TestCPUEmitsNoAccelerator(t *testing.T) {
	p := newTestProvider(t)
	cpu := qemu.NewCPU(p, qemu.WithCPUModel(qemu.CPUHost))
	args := cpu.Args()

	if slices.Contains(args, "-enable-kvm") {
		t.Errorf("CPU should not emit -enable-kvm (accel is machine-level): %v", args)
	}
	if slices.Contains(args, "-accel") {
		t.Errorf("CPU should not emit -accel (accel is machine-level): %v", args)
	}
}

func TestCPUGamingFlags(t *testing.T) {
	p := newTestProvider(t)
	cpu := qemu.NewCPU(p,
		qemu.WithCPUModel(qemu.CPUHost),
		qemu.WithCPUFlags(qemu.GamingCPUFlags),
	)
	args := cpu.Args()
	joined := strings.Join(args, " ")

	for _, flag := range qemu.GamingCPUFlags {
		if !strings.Contains(joined, flag) {
			t.Errorf("missing gaming flag %q in: %s", flag, joined)
		}
	}
	if !strings.Contains(joined, "hv-vendor-id=AMDKVMAMD") {
		t.Errorf("missing AMD vendor spoof in: %s", joined)
	}
}

func TestCPUNoFlags(t *testing.T) {
	p := newTestProvider(t)
	cpu := qemu.NewCPU(p, qemu.WithCPUModel(qemu.CPUHost))
	args := cpu.Args()

	idx := indexOf(args, "-cpu")
	if idx < 0 || idx+1 >= len(args) {
		t.Fatalf("no -cpu arg pair: %v", args)
	}
	cpuVal := args[idx+1]
	if strings.Contains(cpuVal, ",") {
		t.Errorf("no flags expected, but cpu value has comma: %q", cpuVal)
	}
}

func TestCPUDefaultsFromProvider(t *testing.T) {
	p := newTestProvider(t)
	cpu := qemu.NewCPU(p)
	args := cpu.Args()
	joined := strings.Join(args, " ")

	// provider sets DefaultCPUModel="host" and DefaultCPUs=2
	if !strings.Contains(joined, "host") {
		t.Errorf("expected provider default cpu model 'host' in: %s", joined)
	}
	if !strings.Contains(joined, "2") {
		t.Errorf("expected provider default cpu count 2 in smp: %s", joined)
	}
}

// indexOf returns the index of elem in s, or -1.
func indexOf(s []string, elem string) int {
	for i, v := range s {
		if v == elem {
			return i
		}
	}
	return -1
}
