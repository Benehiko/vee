package gpu

import (
	"fmt"
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

// SiblingFunctions returns the other PCI functions on the same physical
// device as addr (i.e. same domain:bus:slot, different function number).
// Unlike IOMMUGroupPeers this is independent of IOMMU group membership —
// kernels with PCIe ACS support can place sibling functions in different
// groups, but qemu still needs them all attached together so that bus-level
// FLR works. Returns nil if addr is malformed or has no siblings on the host.
func SiblingFunctions(addr string) []PCIDevice {
	pciAddr := normalizePCIAddr(addr)
	dot := strings.LastIndex(pciAddr, ".")
	if dot < 0 || dot+2 > len(pciAddr) {
		return nil
	}
	prefix := pciAddr[:dot+1]

	var siblings []PCIDevice
	for fn := 0; fn < 8; fn++ {
		sibAddr := fmt.Sprintf("%s%d", prefix, fn)
		if sibAddr == pciAddr {
			continue
		}
		if _, err := os.Stat(filepath.Join("/sys/bus/pci/devices", sibAddr)); err != nil {
			continue
		}
		siblings = append(siblings, readPCIDevice(sibAddr))
	}
	return siblings
}
