package gpu

import (
	"os"
	"path/filepath"
	"strings"
)

// Vendor is the detected host GPU manufacturer.
type Vendor string

const (
	VendorAMD     Vendor = "amd"
	VendorNvidia  Vendor = "nvidia"
	VendorIntel   Vendor = "intel"
	VendorVirtio  Vendor = "virtio"
	VendorUnknown Vendor = "unknown"
)

// pciVendorIDs maps PCI vendor hex IDs to Vendor constants.
var pciVendorIDs = map[string]Vendor{
	"1002": VendorAMD,    // Advanced Micro Devices
	"10de": VendorNvidia, // NVIDIA Corporation
	"8086": VendorIntel,  // Intel Corporation
	"1af4": VendorVirtio, // Red Hat (virtio)
	"1b36": VendorVirtio, // QEMU virtual GPU
}

// DetectHostGPU scans /sys/bus/pci/devices for the first GPU-class device
// and returns its vendor. Returns VendorUnknown if no GPU is found or the
// vendor is unrecognised.
//
// Only the primary (display-class) GPU bound to a non-vfio driver is
// considered — VFIO-bound devices are already passed through and not the host GPU.
func DetectHostGPU() Vendor {
	entries, err := os.ReadDir("/sys/bus/pci/devices")
	if err != nil {
		return VendorUnknown
	}

	for _, e := range entries {
		addr := e.Name()
		base := filepath.Join("/sys/bus/pci/devices", addr)

		class := strings.TrimSpace(readSysStr(filepath.Join(base, "class")))
		classHex := strings.TrimPrefix(strings.ToLower(class), "0x")
		// PCI class 0300 = VGA compatible, 0302 = 3D controller.
		if !strings.HasPrefix(classHex, "0300") && !strings.HasPrefix(classHex, "0302") {
			continue
		}

		// Skip devices bound to vfio-pci (already passed through).
		driverLink := filepath.Join(base, "driver")
		if target, err := os.Readlink(driverLink); err == nil {
			if filepath.Base(target) == "vfio-pci" {
				continue
			}
		}

		vendorRaw := strings.ToLower(strings.TrimPrefix(
			strings.TrimSpace(readSysStr(filepath.Join(base, "vendor"))),
			"0x",
		))
		if v, ok := pciVendorIDs[vendorRaw]; ok {
			return v
		}
		return VendorUnknown
	}
	return VendorUnknown
}

func readSysStr(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
