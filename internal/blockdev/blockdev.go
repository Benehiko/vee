// Package blockdev enumerates block devices from sysfs.
package blockdev

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
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
	// SizeBytes is the device capacity in bytes (0 if unknown).
	SizeBytes uint64
	// Serial is the disk serial parsed from the by-id link (after the last _).
	Serial string
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
			Name:      name,
			ByIDPath:  "/dev/" + name,
			Model:     readModel(name),
			SizeBytes: readSize(name),
		}
		if stable, ok := byID[name]; ok {
			d.ByIDPath = stable
			d.Serial = parseSerial(stable)
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

// readSize returns the device capacity in bytes. /sys/block/<n>/size is a
// count of 512-byte sectors regardless of the underlying logical block size.
func readSize(name string) uint64 {
	//nolint:gosec // G304: path is confined to /sys/block; name is a kernel device dir entry, not user input.
	b, err := os.ReadFile(filepath.Join("/sys/block", name, "size"))
	if err != nil {
		return 0
	}
	sectors, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return sectors * 512
}

// parseSerial extracts the trailing serial token from a by-id path. by-id
// names look like ata-<model>_<serial> or nvme-<model>_<serial>; the segment
// after the final underscore is conventionally the unit serial.
func parseSerial(byIDPath string) string {
	base := filepath.Base(byIDPath)
	if i := strings.LastIndex(base, "_"); i >= 0 && i+1 < len(base) {
		return base[i+1:]
	}
	return ""
}

// humanSize formats a byte count as a short human-readable string (e.g.
// "22TB", "1.8TB"). Uses powers of 1000 to match disk-vendor labelling.
func humanSize(b uint64) string {
	if b == 0 {
		return ""
	}
	const unit = 1000
	if b < unit {
		return strconv.FormatUint(b, 10) + "B"
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	suffix := []string{"KB", "MB", "GB", "TB", "PB"}[exp]
	whole := b / div
	frac := (b % div) * 10 / div
	if frac == 0 || whole >= 100 {
		return strconv.FormatUint(whole, 10) + suffix
	}
	return strconv.FormatUint(whole, 10) + "." + strconv.FormatUint(frac, 10) + suffix
}

func readModel(name string) string {
	// NVMe: /sys/block/nvme0n1/device/model  (via namespace → ctrl symlink)
	// SATA: /sys/block/sda/device/model
	paths := []string{
		filepath.Join("/sys/block", name, "device/model"),
		filepath.Join("/sys/block", name, "device/device/model"),
	}
	for _, p := range paths {
		//nolint:gosec // G304: path is confined to /sys/block; name is a kernel device dir entry, not user input.
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
	if desc := d.DescribeShort(); desc != "" {
		label += "  " + desc
	}
	return label
}

// DescribeShort returns a compact human description: "<size> <model> [<serial>]".
// Empty fields are skipped, so a device with no by-id link still renders the
// size + model. Use this for shell-completion descriptions and TUI hints.
func (d Device) DescribeShort() string {
	parts := make([]string, 0, 3)
	if size := humanSize(d.SizeBytes); size != "" {
		parts = append(parts, size)
	}
	if d.Model != "" {
		parts = append(parts, d.Model)
	}
	if d.Serial != "" {
		parts = append(parts, "["+d.Serial+"]")
	}
	return strings.Join(parts, " ")
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
