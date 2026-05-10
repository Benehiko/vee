// Package blockdev enumerates block devices from sysfs.
package blockdev

import (
	"bufio"
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

// ListUnmounted returns all block devices that have no mounted partitions or
// filesystems, making them candidates for passthrough. Whole-disk devices whose
// partitions are mounted are also excluded.
func ListUnmounted() ([]Device, error) {
	all, err := ListAll()
	if err != nil {
		return nil, err
	}
	mounted, err := mountedKernelNames()
	if err != nil {
		return nil, err
	}
	var out []Device
	for _, d := range all {
		if !mounted[d.Name] {
			out = append(out, d)
		}
	}
	return out, nil
}

// mountedKernelNames reads /proc/mounts and returns the set of kernel device
// names (e.g. "sda", "nvme0n1") that appear in any mount entry, including
// partition parents — so if sda1 is mounted, "sda" is in the set.
func mountedKernelNames() (map[string]bool, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	used := make(map[string]bool)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		dev := fields[0]
		// Only care about real block devices (/dev/...).
		if !strings.HasPrefix(dev, "/dev/") {
			continue
		}
		kernel := filepath.Base(dev)
		used[kernel] = true
		// Also mark the parent disk (strip trailing digits and "p" partition suffix).
		parent := parentDisk(kernel)
		if parent != "" {
			used[parent] = true
		}
	}
	return used, sc.Err()
}

// parentDisk strips the partition suffix from a kernel device name.
// "sda1" → "sda", "nvme0n1p3" → "nvme0n1", "nvme0n1" → "".
func parentDisk(name string) string {
	// NVMe partitions end in "p<digit>".
	if i := strings.LastIndex(name, "p"); i > 0 {
		if allDigits(name[i+1:]) {
			return name[:i]
		}
	}
	// SATA/SCSI partitions end in digits.
	i := len(name)
	for i > 0 && name[i-1] >= '0' && name[i-1] <= '9' {
		i--
	}
	if i < len(name) {
		return name[:i]
	}
	return ""
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
