package qemu_test

import (
	"testing"

	"github.com/Benehiko/vee/internal/qemu"
)

func TestHeliosGPUDevice(t *testing.T) {
	cases := []struct {
		hostMem, want string
	}{
		{"16G", "virtio-gpu-gl-pci,id=heliosgpu,max_outputs=1,blob=true,venus=true,hostmem=16G,max_hostmem=16G"},
		{"", "virtio-gpu-gl-pci,id=heliosgpu,max_outputs=1,blob=true,venus=true,hostmem=" + qemu.DefaultVenusHostMem + ",max_hostmem=" + qemu.DefaultVenusHostMem},
	}
	for _, c := range cases {
		if got := qemu.HeliosGPUDevice(c.hostMem); got != c.want {
			t.Errorf("HeliosGPUDevice(%q) = %q, want %q", c.hostMem, got, c.want)
		}
	}
}

func TestHeliosDisplay(t *testing.T) {
	if got := qemu.HeliosDisplay(""); got != "egl-headless" {
		t.Errorf("HeliosDisplay(\"\") = %q", got)
	}
	if got := qemu.HeliosDisplay("/dev/dri/renderD129"); got != "egl-headless,rendernode=/dev/dri/renderD129" {
		t.Errorf("HeliosDisplay(node) = %q", got)
	}
}

func TestHeliosVNCArg(t *testing.T) {
	cases := map[string]string{
		"":                         qemu.DefaultHeliosVNC + ",share=force-shared",
		"127.0.0.1:48":             "127.0.0.1:48,share=force-shared",
		"127.0.0.1:3,share=ignore": "127.0.0.1:3,share=ignore",
	}
	for in, want := range cases {
		if got := qemu.HeliosVNCArg(in); got != want {
			t.Errorf("HeliosVNCArg(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVNCHostPort(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{"", "127.0.0.1:5900", false},
		{"127.0.0.1:12", "127.0.0.1:5912", false},
		{":3", "localhost:5903", false},
		{"127.0.0.1:48,share=force-shared", "127.0.0.1:5948", false},
		{"127.0.0.1", "", true},
		{"127.0.0.1:x", "", true},
	}
	for _, c := range cases {
		got, err := qemu.VNCHostPort(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("VNCHostPort(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("VNCHostPort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
