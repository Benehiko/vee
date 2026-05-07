package qemu

import "fmt"

// VFIODevice represents a PCI device passed through to the guest via VFIO.
// A PCIe root port is automatically emitted before the device.
type VFIODevice struct {
	// PCIAddr is the host PCI address, e.g. "0000:08:00.0".
	PCIAddr string
	// BusID is the QEMU PCIe root port ID, e.g. "pcie.1".
	BusID         string
	ROMBar        bool
	Multifunction bool
}

var _ Builder = &VFIODevice{}

func NewVFIODevice(pciAddr string) *VFIODevice {
	return &VFIODevice{
		PCIAddr:       pciAddr,
		BusID:         "pcie.1",
		ROMBar:        false,
		Multifunction: true,
	}
}

func (v *VFIODevice) Args() []string {
	rootPort := fmt.Sprintf("pcie-root-port,id=%s,slot=1", v.BusID)
	device := fmt.Sprintf("vfio-pci,host=%s,bus=%s,multifunction=%s",
		v.PCIAddr, v.BusID, boolOnOff(v.Multifunction))
	if !v.ROMBar {
		device += ",rombar=0"
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
