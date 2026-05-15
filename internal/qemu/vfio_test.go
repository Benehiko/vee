package qemu_test

import (
	"strings"
	"testing"

	"github.com/Benehiko/vee/internal/qemu"
)

func TestSameSlot(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0000:08:00.0", "0000:08:00.1", true},
		{"0000:08:00.0", "0000:08:00.0", false}, // same address, not siblings
		{"0000:08:00.0", "0000:09:00.0", false},
		{"0000:08:00.0", "0000:08:01.0", false},
		{"0000:08:00.0", "", false},
	}
	for _, tc := range cases {
		if got := qemu.SameSlot(tc.a, tc.b); got != tc.want {
			t.Errorf("SameSlot(%q, %q) = %v; want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestFunctionNumber(t *testing.T) {
	cases := []struct {
		addr string
		want int
	}{
		{"0000:08:00.0", 0},
		{"0000:08:00.1", 1},
		{"0000:08:00.7", 7},
		{"0000:08:00", 0},
		{"", 0},
	}
	for _, tc := range cases {
		if got := qemu.FunctionNumber(tc.addr); got != tc.want {
			t.Errorf("FunctionNumber(%q) = %d; want %d", tc.addr, got, tc.want)
		}
	}
}

func TestVFIODevicePrimaryArgs(t *testing.T) {
	d := qemu.NewVFIODevice("0000:08:00.0")
	args := strings.Join(d.Args(), " ")
	for _, want := range []string{
		"pcie-root-port,id=pcie.1,slot=1",
		"vfio-pci,host=0000:08:00.0,bus=pcie.1,addr=0x0",
		"multifunction=on",
		"rombar=0",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("primary args missing %q\nfull: %s", want, args)
		}
	}
}

func TestVFIODeviceSiblingArgs(t *testing.T) {
	d := qemu.NewVFIOSiblingFunction("0000:08:00.1", "pcie.1", 1)
	args := strings.Join(d.Args(), " ")
	if strings.Contains(args, "pcie-root-port") {
		t.Errorf("sibling must not emit its own pcie-root-port: %s", args)
	}
	for _, want := range []string{
		"vfio-pci,host=0000:08:00.1,bus=pcie.1,addr=0.1",
		"rombar=0",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("sibling args missing %q\nfull: %s", want, args)
		}
	}
	if strings.Contains(args, "multifunction=on") {
		t.Errorf("sibling must not advertise multifunction=on: %s", args)
	}
}

func TestVFIODevicePeerArgs(t *testing.T) {
	d := qemu.NewVFIOPeerDevice("0000:09:00.0", "pcie.2", 2)
	args := strings.Join(d.Args(), " ")
	for _, want := range []string{
		"pcie-root-port,id=pcie.2,slot=2",
		"vfio-pci,host=0000:09:00.0,bus=pcie.2,addr=0x0",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("peer args missing %q\nfull: %s", want, args)
		}
	}
	if strings.Contains(args, "multifunction=on") {
		t.Errorf("peer must not advertise multifunction=on: %s", args)
	}
}
