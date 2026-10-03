package vm

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func heliosTestConfig(t *testing.T) *VMConfig {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "qemu-system-x86_64")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &VMConfig{
		Name:       "heliosvm",
		Template:   "windows",
		Memory:     "4G",
		CPUs:       2,
		Sockets:    1,
		Cores:      2,
		Threads:    1,
		QemuBinary: bin,
		Disks: []DiskConfig{{
			Path: filepath.Join(t.TempDir(), "os.qcow2"), Size: "1G", Format: "qcow2",
			Interface: "virtio", Media: "disk", Cache: "writeback",
		}},
		GPU: GPUConfig{Mode: GPUHelios, HostMem: "12G", VNC: "127.0.0.1:42", RenderNode: "/dev/dri/renderD129"},
	}
}

func TestBuildMachineHelios(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("GPU mode helios needs a linux/amd64 host")
	}
	m := newTestManager(t)
	cfg := heliosTestConfig(t)
	cfg.InstallDevices = []string{"VGA,id=bootstrap-gpu,bus=pcie.0,addr=0x1"}
	machine, _, err := m.buildMachine(t.Context(), cfg)
	if err != nil {
		t.Fatalf("buildMachine: %v", err)
	}
	args := strings.Join(machine.Args(), " ")
	for _, want := range []string{
		"-device virtio-gpu-gl-pci,id=heliosgpu,max_outputs=1,blob=true,venus=true,hostmem=12G,max_hostmem=12G",
		"-display egl-headless,rendernode=/dev/dri/renderD129",
		"-vnc 127.0.0.1:42,share=force-shared",
		"-vga none",
		"-device VGA,id=bootstrap-gpu,bus=pcie.0,addr=0x1",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args missing %q\nargs: %s", want, args)
		}
	}
	if strings.Contains(args, "virtio-vga-gl") || strings.Contains(args, "gtk") {
		t.Errorf("helios VM got the generic virtio GL device/display:\n%s", args)
	}
}

func TestBuildMachineHeliosNeedsQemuBinary(t *testing.T) {
	m := newTestManager(t)
	cfg := heliosTestConfig(t)
	cfg.QemuBinary = ""
	if _, _, err := m.buildMachine(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "qemu-helios") {
		t.Errorf("err = %v, want a qemu-helios hint", err)
	}
	cfg.QemuBinary = filepath.Join(t.TempDir(), "missing")
	if _, _, err := m.buildMachine(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "qemu_binary") {
		t.Errorf("err = %v, want a qemu_binary stat error", err)
	}
}

func TestBuildMachineRejectsBadQemuEnv(t *testing.T) {
	m := newTestManager(t)
	cfg := heliosTestConfig(t)
	cfg.QemuEnv = []string{"not-an-assignment"}
	if _, _, err := m.buildMachine(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "qemu_env") {
		t.Errorf("err = %v, want a qemu_env error", err)
	}
}
