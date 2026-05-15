package qemu

import (
	"fmt"
	"strings"
)

// VFIODevice represents a PCI device passed through to the guest via VFIO.
//
// A VFIODevice may either own its own pcie-root-port (the typical case
// for a discrete GPU or an unrelated peer like a NIC), or piggy-back on
// an existing root port as a *sibling function* of the device that owns
// that port (the typical case for a GPU's HDMI/DP audio function, which
// must sit on the same PCIe slot as the VGA function so the guest sees
// them as one multifunction device).
type VFIODevice struct {
	// PCIAddr is the host PCI address, e.g. "0000:08:00.0".
	PCIAddr string
	// BusID is the QEMU PCIe root port ID, e.g. "pcie.1".
	BusID string
	// Slot is the chassis slot number for the PCIe root port — must be
	// unique per machine. Ignored when EmitRootPort is false.
	Slot int
	// EmitRootPort is true when this device should emit its own
	// pcie-root-port. Set to false for sibling functions sharing the
	// root port emitted by another VFIODevice.
	EmitRootPort bool
	// GuestFunction is the function number this device should appear as
	// inside the guest (0..7). For the primary function on a multifunction
	// device this is 0; for sibling functions it must match the host
	// function so the guest BIOS/firmware sees a coherent multifunction
	// layout.
	GuestFunction int
	// ROMBar controls whether QEMU exposes the ROM BAR to the guest.
	// Should be true for GPU primary functions only when supplying a
	// VBIOS via ROMFile; AMD Navi GPUs return an invalid ROM signature
	// when probed through vfio-pci without an explicit dump.
	ROMBar bool
	// ROMFile is an optional path to a VBIOS dump (romfile= QEMU argument).
	ROMFile string
	// Multifunction marks this device as the primary (function 0) of a
	// multifunction device — sibling functions share the same root port
	// but should leave Multifunction=false.
	Multifunction bool
}

var _ Builder = &VFIODevice{}

// NewVFIODevice creates a primary VFIO device (typically a GPU) on its
// own PCIe root port, advertised as the first function of a
// multifunction device so sibling functions can attach via
// NewVFIOSiblingFunction.
func NewVFIODevice(pciAddr string) *VFIODevice {
	return &VFIODevice{
		PCIAddr:       pciAddr,
		BusID:         "pcie.1",
		Slot:          1,
		EmitRootPort:  true,
		GuestFunction: 0,
		ROMBar:        false,
		Multifunction: true,
	}
}

// NewVFIOPeerDevice creates a secondary VFIO device that lives on its
// own dedicated PCIe root port. Use this for unrelated peer devices
// (e.g. a passthrough NIC) — for the audio function that shares a
// physical device with a GPU, use NewVFIOSiblingFunction instead so
// QEMU sees both functions on the same slot.
func NewVFIOPeerDevice(pciAddr, busID string, slot int) *VFIODevice {
	return &VFIODevice{
		PCIAddr:       pciAddr,
		BusID:         busID,
		Slot:          slot,
		EmitRootPort:  true,
		GuestFunction: 0,
		ROMBar:        false,
		Multifunction: false,
	}
}

// NewVFIOSiblingFunction creates a secondary VFIO device that shares the
// PCIe root port of another VFIODevice — for the HDMI/DP audio function
// of a discrete GPU, where both functions live on the same physical
// device on the host (e.g. 0000:08:00.0 and 0000:08:00.1) and must sit
// on the same pcie-root-port in the guest at the matching function
// number. Without this, QEMU's pci_irq_handler asserts because the IRQ
// pin maps for the two functions disagree.
//
// busID is the BusID of the primary VFIODevice (e.g. "pcie.1").
// fn is the guest function number, typically derived from the host
// PCI function number (08:00.1 → fn=1).
func NewVFIOSiblingFunction(pciAddr, busID string, fn int) *VFIODevice {
	return &VFIODevice{
		PCIAddr:       pciAddr,
		BusID:         busID,
		EmitRootPort:  false,
		GuestFunction: fn,
		ROMBar:        false,
		Multifunction: false,
	}
}

func (v *VFIODevice) Args() []string {
	// On a pcie-root-port the parent bus only exposes slot 0.
	// Sibling functions (fn > 0) must use addr=0.N (slot.function) — not addr=0xN.
	addr := "addr=0x0"
	if v.GuestFunction > 0 {
		addr = fmt.Sprintf("addr=0.%d", v.GuestFunction)
	}
	device := fmt.Sprintf("vfio-pci,host=%s,bus=%s,%s",
		v.PCIAddr, v.BusID, addr)
	if v.Multifunction {
		device += ",multifunction=on"
	}
	if !v.ROMBar {
		device += ",rombar=0"
	}
	if v.ROMFile != "" {
		device += fmt.Sprintf(",romfile=%s", v.ROMFile)
	}

	if !v.EmitRootPort {
		return []string{"-device", device}
	}
	rootPort := fmt.Sprintf("pcie-root-port,id=%s,slot=%d", v.BusID, v.Slot)
	return []string{
		"-device", rootPort,
		"-device", device,
	}
}

// FunctionNumber returns the host PCI function number from a normalised
// address like "0000:08:00.1" — i.e. the digit after the dot. Returns
// 0 if the address is malformed or has no function suffix.
func FunctionNumber(pciAddr string) int {
	dot := strings.LastIndex(pciAddr, ".")
	if dot < 0 || dot+1 >= len(pciAddr) {
		return 0
	}
	var fn int
	if _, err := fmt.Sscanf(pciAddr[dot+1:], "%d", &fn); err != nil {
		return 0
	}
	return fn
}

// SameSlot reports whether two normalised PCI addresses describe
// different functions of the same physical device — same domain, bus
// and slot, different function. Used to decide whether a peer should be
// attached as a sibling function on the primary's root port instead of
// getting its own.
func SameSlot(a, b string) bool {
	dotA := strings.LastIndex(a, ".")
	dotB := strings.LastIndex(b, ".")
	if dotA < 0 || dotB < 0 {
		return false
	}
	return a[:dotA] == b[:dotB] && a != b
}
