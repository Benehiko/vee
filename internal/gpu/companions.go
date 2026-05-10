package gpu

import (
	"os"
	"path/filepath"
	"strings"
)

// ListGPUAddresses returns the PCI addresses of all GPU-class devices on the
// host (class 0300 VGA / 0302 3D controller), skipping vfio-pci bound devices.
// Used for shell completion of --gpu-pci.
func ListGPUAddresses() []PCIDevice {
	entries, err := os.ReadDir("/sys/bus/pci/devices")
	if err != nil {
		return nil
	}
	var gpus []PCIDevice
	for _, e := range entries {
		d := readPCIDevice(e.Name())
		if !d.IsGPU {
			continue
		}
		gpus = append(gpus, d)
	}
	return gpus
}

// IOMMUGroupPeers returns the other PCI devices that share the IOMMU group of
// addr. These must all be bound to vfio-pci together when doing passthrough.
// Returns nil if addr has no IOMMU group or the group contains only addr.
func IOMMUGroupPeers(addr string) []PCIDevice {
	pciAddr := normalizePCIAddr(addr)
	groupLink := filepath.Join("/sys/bus/pci/devices", pciAddr, "iommu_group")
	target, err := os.Readlink(groupLink)
	if err != nil {
		return nil
	}
	devicesPath := filepath.Join(target, "devices")
	entries, err := os.ReadDir(devicesPath)
	if err != nil {
		// Fallback: absolute path via iommuGroupsPath.
		groupName := filepath.Base(target)
		devicesPath = filepath.Join(iommuGroupsPath, groupName, "devices")
		entries, err = os.ReadDir(devicesPath)
		if err != nil {
			return nil
		}
	}

	var peers []PCIDevice
	for _, e := range entries {
		if e.Name() == pciAddr {
			continue
		}
		peers = append(peers, readPCIDevice(e.Name()))
	}
	return peers
}

// IsAudioDevice reports whether d is a PCI HD Audio or generic multimedia
// controller (class 0403 or 0480), which is typically the companion HDMI/DP
// audio function of a discrete GPU (e.g. 08:00.1 alongside 08:00.0).
func IsAudioDevice(d PCIDevice) bool {
	classHex := strings.ToLower(strings.TrimPrefix(d.Class, "0x"))
	return strings.HasPrefix(classHex, "0403") || strings.HasPrefix(classHex, "0480")
}
