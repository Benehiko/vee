package gpu

import (
	"bufio"
	"os"
	"strings"
)

var pciIDsPath = "/usr/share/hwdata/pci.ids"

// LookupDeviceName resolves vendor+device hex IDs (e.g. "10de", "2184") to a
// human-readable string like "NVIDIA Corporation / TU116 [GeForce GTX 1660]".
// Returns addr unchanged on any error or miss.
func LookupDeviceName(vendor, device string) string {
	vendor = strings.ToLower(strings.TrimPrefix(vendor, "0x"))
	device = strings.ToLower(strings.TrimPrefix(device, "0x"))

	f, err := os.Open(pciIDsPath)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	var vendorName, deviceName string
	inVendor := false

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		// Vendor line: no leading tab, 4-hex-digit id then space(s) then name.
		if line[0] != '\t' {
			if len(line) < 4 {
				inVendor = false
				continue
			}
			id := strings.ToLower(line[:4])
			if id == vendor {
				name := strings.TrimSpace(line[4:])
				vendorName = name
				inVendor = true
			} else if inVendor {
				// Past our vendor block — stop.
				break
			}
			continue
		}
		if !inVendor {
			continue
		}
		// Device line: single tab, 4-hex id, space(s), name.
		if line[0] == '\t' && (len(line) < 2 || line[1] != '\t') {
			trimmed := strings.TrimPrefix(line, "\t")
			if len(trimmed) < 4 {
				continue
			}
			id := strings.ToLower(trimmed[:4])
			if id == device {
				deviceName = strings.TrimSpace(trimmed[4:])
			}
		}
	}

	switch {
	case vendorName != "" && deviceName != "":
		return vendorName + " / " + deviceName
	case vendorName != "":
		return vendorName + " / " + device
	default:
		return vendor + ":" + device
	}
}

// GPULabel returns a display label for a PCIDevice.
func GPULabel(d PCIDevice) string {
	name := LookupDeviceName(d.Vendor, d.Device)
	if name == "" {
		name = d.Vendor + ":" + d.Device
	}
	return d.Address + "  " + name
}
