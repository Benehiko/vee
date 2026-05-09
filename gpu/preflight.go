package gpu

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// PreflightResult holds the outcome of every VFIO readiness check for one PCI device.
type PreflightResult struct {
	PCIAddr string

	// Driver currently bound to the device ("vfio-pci" = ready).
	Driver string

	// IOMMU group number (-1 = not found).
	IOMMUGroup int

	// Other devices sharing the same IOMMU group (they must also be bound to vfio-pci).
	GroupPeers []PCIDevice

	// Path to /dev/vfio/<group>, empty if not found.
	VFIODevPath string

	// Whether the current process can open the VFIO device node.
	VFIOAccessible bool

	// Current soft memlock limit in bytes (^uint64(0) = unlimited).
	MemlockSoftBytes uint64

	// Current hard memlock limit in bytes (^uint64(0) = unlimited).
	MemlockHardBytes uint64

	// Required memlock in bytes (== VM RAM size).
	MemlockRequiredBytes uint64

	// Device power and reset state.
	DeviceState DeviceState

	// Errors collected per check — nil entry means the check passed.
	Errors map[string]error
}

// MemlockOK reports whether the soft memlock limit covers the required RAM.
func (r *PreflightResult) MemlockOK() bool {
	const unlimited = ^uint64(0)
	if r.MemlockSoftBytes == unlimited {
		return true
	}
	return r.MemlockRequiredBytes > 0 && r.MemlockSoftBytes >= r.MemlockRequiredBytes
}

// OK returns true only when every check passed.
func (r *PreflightResult) OK() bool {
	for _, err := range r.Errors {
		if err != nil {
			return false
		}
	}
	return true
}

// PreflightCheck inspects the host state for VFIO passthrough of addr.
// memoryStr is the VM RAM string (e.g. "16G"); pass "" to skip the memlock size check.
func PreflightCheck(addr, memoryStr string) *PreflightResult {
	pciAddr := normalizePCIAddr(addr)
	r := &PreflightResult{
		PCIAddr:    pciAddr,
		IOMMUGroup: -1,
		Errors:     make(map[string]error),
	}

	// 0. Power / reset state.
	r.DeviceState = ReadDeviceState(pciAddr)
	if r.DeviceState.NeedsReset() {
		r.Errors["power_state"] = fmt.Errorf(
			"device is in %s/%s — likely stuck from a previous unclean exit; vee will attempt reset before start",
			r.DeviceState.PowerState, r.DeviceState.RuntimeStatus)
	}

	// 1. Driver check.
	r.Driver = CurrentDriver(pciAddr)
	if r.Driver != "vfio-pci" {
		if r.Driver == "" {
			r.Errors["driver"] = fmt.Errorf("no driver bound to %s — run: vee gpu bind %s", pciAddr, addr)
		} else {
			r.Errors["driver"] = fmt.Errorf("device bound to %q, not vfio-pci — run: vee gpu bind %s", r.Driver, addr)
		}
	}

	// 2. IOMMU group + peer devices.
	groupLink := filepath.Join("/sys/bus/pci/devices", pciAddr, "iommu_group")
	if target, err := os.Readlink(groupLink); err == nil {
		base := filepath.Base(target)
		var id int
		if _, err := fmt.Sscanf(base, "%d", &id); err == nil {
			r.IOMMUGroup = id
		}
	}
	if r.IOMMUGroup < 0 {
		r.Errors["iommu_group"] = fmt.Errorf("device %s has no IOMMU group — enable iommu in kernel params (intel_iommu=on or amd_iommu=on)", pciAddr)
	} else {
		devicesPath := filepath.Join(iommuGroupsPath, strconv.Itoa(r.IOMMUGroup), "devices")
		if entries, err := os.ReadDir(devicesPath); err == nil {
			for _, e := range entries {
				if e.Name() == pciAddr {
					continue
				}
				peer := readPCIDevice(e.Name())
				r.GroupPeers = append(r.GroupPeers, peer)
				if peer.Driver != "vfio-pci" {
					r.Errors["iommu_group_peer_"+e.Name()] = fmt.Errorf(
						"peer device %s in same IOMMU group is bound to %q, not vfio-pci — run: vee gpu bind %s",
						e.Name(), peer.Driver, e.Name())
				}
			}
		}
	}

	// 3. /dev/vfio/<group> existence and accessibility.
	if r.IOMMUGroup >= 0 {
		r.VFIODevPath = fmt.Sprintf("/dev/vfio/%d", r.IOMMUGroup)
		if _, err := os.Stat(r.VFIODevPath); err != nil {
			r.Errors["vfio_dev"] = fmt.Errorf("%s does not exist — is vfio-pci loaded? (modprobe vfio-pci)", r.VFIODevPath)
		} else {
			f, err := os.OpenFile(r.VFIODevPath, os.O_RDWR, 0)
			if err != nil {
				r.VFIOAccessible = false
				r.Errors["vfio_access"] = fmt.Errorf("cannot open %s: %v — add user to vfio group: sudo usermod -aG vfio $USER", r.VFIODevPath, err)
			} else {
				_ = f.Close()
				r.VFIOAccessible = true
			}
		}
	}

	// 4. Memlock limit.
	var rLimit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_MEMLOCK, &rLimit); err == nil {
		r.MemlockSoftBytes = rLimit.Cur
		r.MemlockHardBytes = rLimit.Max
	}
	if memoryStr != "" {
		if bytes, err := parseMemoryBytes(memoryStr); err == nil {
			r.MemlockRequiredBytes = bytes
			if !r.MemlockOK() {
				r.Errors["memlock"] = fmt.Errorf(
					"memlock soft limit %s < required %s — add to /etc/security/limits.d/vee-vfio.conf: '* - memlock unlimited'",
					FormatBytes(r.MemlockSoftBytes), FormatBytes(r.MemlockRequiredBytes))
			}
		}
	}

	return r
}

// parseMemoryBytes converts QEMU memory strings ("16G", "4096M", "1024") to bytes.
func parseMemoryBytes(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty memory string")
	}
	suffix := s[len(s)-1]
	switch suffix {
	case 'G', 'g':
		n, err := strconv.ParseUint(s[:len(s)-1], 10, 64)
		return n * 1024 * 1024 * 1024, err
	case 'M', 'm':
		n, err := strconv.ParseUint(s[:len(s)-1], 10, 64)
		return n * 1024 * 1024, err
	case 'K', 'k':
		n, err := strconv.ParseUint(s[:len(s)-1], 10, 64)
		return n * 1024, err
	default:
		return strconv.ParseUint(s, 10, 64)
	}
}

func FormatBytes(b uint64) string {
	const unlimited = ^uint64(0)
	if b == unlimited {
		return "unlimited"
	}
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.0fG", float64(b)/float64(1024*1024*1024))
	case b >= 1024*1024:
		return fmt.Sprintf("%.0fM", float64(b)/float64(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.0fK", float64(b)/float64(1024))
	default:
		return fmt.Sprintf("%dB", b)
	}
}
