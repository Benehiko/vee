// Package blockdev enumerates block devices from sysfs.
package blockdev

import (
	"os"
	"path/filepath"
	"strings"
)

// Device represents a host block device.
type Device struct {
	// Name is the kernel name, e.g. "nvme0n1".
	Name string
	// ByIDPath is the stable /dev/disk/by-id/ path if one exists, otherwise /dev/<Name>.
	ByIDPath string
	// Model is read from /sys/block/<name>/device/model (may be empty).
	Model string
}

// ListNVMe returns all NVMe namespaces visible in /sys/block.
func ListNVMe() ([]Device, error) {
	return listByPrefix("nvme")
}

// ListAll returns all block devices from /sys/block.
func ListAll() ([]Device, error) {
	return listByPrefix("")
}

func listByPrefix(prefix string) ([]Device, error) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, err
	}

	byID, _ := buildByIDMap()

	var devs []Device
	for _, e := range entries {
		name := e.Name()
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		d := Device{
			Name:     name,
			ByIDPath: "/dev/" + name,
			Model:    readModel(name),
		}
		if stable, ok := byID[name]; ok {
			d.ByIDPath = stable
		}
		devs = append(devs, d)
	}
	return devs, nil
}

// buildByIDMap maps kernel device name → first /dev/disk/by-id/ path.
func buildByIDMap() (map[string]string, error) {
	entries, err := os.ReadDir("/dev/disk/by-id")
	if err != nil {
		return nil, err
	}
	m := make(map[string]string)
	for _, e := range entries {
		link := filepath.Join("/dev/disk/by-id", e.Name())
		target, err := os.Readlink(link)
		if err != nil {
			continue
		}
		// target is e.g. "../../nvme0n1" or "../../sda"
		kernel := filepath.Base(target)
		// Prefer the first (shortest/simplest) entry for each device.
		if _, exists := m[kernel]; !exists {
			m[kernel] = link
		}
	}
	return m, nil
}

func readModel(name string) string {
	// NVMe: /sys/block/nvme0n1/device/model  (via namespace → ctrl symlink)
	// SATA: /sys/block/sda/device/model
	paths := []string{
		filepath.Join("/sys/block", name, "device/model"),
		filepath.Join("/sys/block", name, "device/device/model"),
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

// Label returns a display string for the device.
func (d Device) Label() string {
	label := d.ByIDPath
	if d.Model != "" {
		label += "  [" + d.Model + "]"
	}
	return label
}
