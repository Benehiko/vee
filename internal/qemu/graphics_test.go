package qemu_test

import (
	"testing"

	"github.com/Benehiko/vee/internal/qemu"
)

func TestVirtioGPUDevice(t *testing.T) {
	cases := []struct {
		name    string
		arch    string
		gl      bool
		venus   bool
		hostMem string
		want    string
	}{
		{"aarch64 gl", "aarch64", true, false, "", "virtio-gpu-gl-pci"},
		{"arm64 alias gl", "arm64", true, false, "", "virtio-gpu-gl-pci"},
		{"x86_64 gl", "x86_64", true, false, "", "virtio-vga-gl"},
		{"aarch64 no gl", "aarch64", false, false, "", "virtio-gpu-pci"},
		{"x86_64 no gl", "x86_64", false, false, "", "virtio-gpu-pci"},
		{"aarch64 venus", "aarch64", true, true, "8G", "virtio-gpu-gl-pci,blob=true,venus=true,hostmem=8G"},
		{"venus without hostmem", "aarch64", true, true, "", "virtio-gpu-gl-pci,blob=true,venus=true"},
		{"venus ignored without gl", "aarch64", false, true, "8G", "virtio-gpu-pci"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := qemu.VirtioGPUDevice(c.arch, c.gl, c.venus, c.hostMem)
			if got != c.want {
				t.Errorf("VirtioGPUDevice(%q, gl=%v, venus=%v, %q) = %q, want %q",
					c.arch, c.gl, c.venus, c.hostMem, got, c.want)
			}
		})
	}
}

func TestDisplayArg(t *testing.T) {
	cases := []struct {
		name    string
		hostOS  string
		gl      bool
		backend qemu.GLBackend
		want    string
	}{
		{"macOS gl default", "darwin", true, "", "cocoa,gl=es"},
		{"macOS gl core", "darwin", true, qemu.GLBackendCore, "cocoa,gl=core"},
		{"macOS no gl", "darwin", false, "", "cocoa"},
		{"linux gl default", "linux", true, "", "gtk,gl=on"},
		{"linux no gl", "linux", false, "", "gtk"},
		{"explicit es on linux", "linux", true, qemu.GLBackendES, "gtk,gl=es"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := qemu.DisplayArg(c.hostOS, c.gl, c.backend)
			if got != c.want {
				t.Errorf("DisplayArg(%q, gl=%v, %q) = %q, want %q",
					c.hostOS, c.gl, c.backend, got, c.want)
			}
		})
	}
}

func TestDefaultGLBackend(t *testing.T) {
	if got := qemu.DefaultGLBackend("darwin"); got != qemu.GLBackendES {
		t.Errorf("macOS default GL backend: got %q, want es", got)
	}
	if got := qemu.DefaultGLBackend("linux"); got != qemu.GLBackendOn {
		t.Errorf("linux default GL backend: got %q, want on", got)
	}
}

func TestAppleGFXDevice(t *testing.T) {
	if got := qemu.AppleGFXDevice("aarch64"); got != "apple-gfx-mmio" {
		t.Errorf("aarch64 apple-gfx device: got %q, want apple-gfx-mmio", got)
	}
	if got := qemu.AppleGFXDevice("arm64"); got != "apple-gfx-mmio" {
		t.Errorf("arm64 apple-gfx device: got %q, want apple-gfx-mmio", got)
	}
	if got := qemu.AppleGFXDevice("x86_64"); got != "apple-gfx-pci" {
		t.Errorf("x86_64 apple-gfx device: got %q, want apple-gfx-pci", got)
	}
}
