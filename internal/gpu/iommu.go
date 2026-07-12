package gpu

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const iommuGroupsPath = "/sys/kernel/iommu_groups"

type PCIDevice struct {
	Address string
	Vendor  string
	Device  string
	Class   string
	Driver  string
	IsGPU   bool
}

type IOMMUGroup struct {
	ID      int
	Devices []PCIDevice
}

// ListIOMMUGroups enumerates all IOMMU groups and their PCI devices.
// Returns an error if IOMMU is not enabled on the system.
func ListIOMMUGroups() ([]IOMMUGroup, error) {
	if _, err := os.Stat(iommuGroupsPath); err != nil {
		return nil, fmt.Errorf("IOMMU not available: %w (enable intel_iommu=on or amd_iommu=on in kernel params)", err)
	}

	entries, err := os.ReadDir(iommuGroupsPath)
	if err != nil {
		return nil, err
	}

	var groups []IOMMUGroup
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var id int
		if _, err := fmt.Sscanf(e.Name(), "%d", &id); err != nil {
			continue
		}

		devicesPath := filepath.Join(iommuGroupsPath, e.Name(), "devices")
		devEntries, err := os.ReadDir(devicesPath)
		if err != nil {
			continue
		}

		var devices []PCIDevice
		for _, dev := range devEntries {
			addr := dev.Name()
			d := readPCIDevice(addr)
			devices = append(devices, d)
		}

		groups = append(groups, IOMMUGroup{ID: id, Devices: devices})
	}
	return groups, nil
}

func readPCIDevice(addr string) PCIDevice {
	base := filepath.Join("/sys/bus/pci/devices", addr)
	d := PCIDevice{Address: addr}

	d.Vendor = readSysFile(filepath.Join(base, "vendor"))
	d.Device = readSysFile(filepath.Join(base, "device"))
	d.Class = readSysFile(filepath.Join(base, "class"))

	// Resolve driver symlink → just the last path component.
	driverLink := filepath.Join(base, "driver")
	if target, err := os.Readlink(driverLink); err == nil {
		d.Driver = filepath.Base(target)
	}

	// PCI class 0x0300 = VGA compatible controller, 0x0302 = 3D controller.
	classHex := strings.TrimPrefix(d.Class, "0x")
	if strings.HasPrefix(classHex, "0300") || strings.HasPrefix(classHex, "0302") {
		d.IsGPU = true
	}

	return d
}

func readSysFile(path string) string {
	b, err := os.ReadFile(path) //nolint:gosec // path is a fixed /sys/bus/pci sysfs entry built from a normalized PCI address, not user input
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
