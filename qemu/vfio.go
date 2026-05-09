package qemu

import "fmt"

// VFIODevice represents a PCI device passed through to the guest via VFIO.
// A PCIe root port is automatically emitted before the device.
type VFIODevice struct {
	// PCIAddr is the host PCI address, e.g. "0000:08:00.0".
	PCIAddr string
	// BusID is the QEMU PCIe root port ID, e.g. "pcie.1".
	BusID string
	// Slot is the chassis slot number for the PCIe root port — must be unique per machine.
	Slot int
	// ROMBar controls whether QEMU exposes the ROM BAR to the guest.
	// Should be true for GPU primary functions so the guest driver can read the VBIOS.
	ROMBar bool
	// ROMFile is an optional path to a VBIOS dump (romfile= QEMU argument).
	ROMFile       string
	Multifunction bool
}

var _ Builder = &VFIODevice{}

// NewVFIODevice creates a primary VFIO device (GPU) with rombar disabled.
// AMD Navi GPUs return an invalid ROM signature when probed through vfio-pci
// without a VBIOS dump; enabling the ROM BAR can cause the device to hang
// during reset. Use GPUConfig.ROMFile to supply a VBIOS explicitly instead.
func NewVFIODevice(pciAddr string) *VFIODevice {
	return &VFIODevice{
		PCIAddr:       pciAddr,
		BusID:         "pcie.1",
		Slot:          1,
		ROMBar:        false,
		Multifunction: true,
	}
}

// NewVFIOPeerDevice creates a secondary VFIO device (e.g. GPU audio function)
// on its own PCIe root port. busID and slot must be unique across all devices in the machine.
func NewVFIOPeerDevice(pciAddr, busID string, slot int) *VFIODevice {
	return &VFIODevice{
		PCIAddr:       pciAddr,
		BusID:         busID,
		Slot:          slot,
		ROMBar:        false,
		Multifunction: false,
	}
}

func (v *VFIODevice) Args() []string {
	rootPort := fmt.Sprintf("pcie-root-port,id=%s,slot=%d", v.BusID, v.Slot)
	device := fmt.Sprintf("vfio-pci,host=%s,bus=%s,multifunction=%s",
		v.PCIAddr, v.BusID, boolOnOff(v.Multifunction))
	if !v.ROMBar {
		device += ",rombar=0"
	}
	if v.ROMFile != "" {
		device += fmt.Sprintf(",romfile=%s", v.ROMFile)
	}
	return []string{
		"-device", rootPort,
		"-device", device,
	}
}

func boolOnOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
