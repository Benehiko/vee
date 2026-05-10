package gpu

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// BindVFIO binds a PCI device to the vfio-pci driver.
// Requires root — returns an error immediately if not running as root.
func BindVFIO(addr string) error {
	if os.Getuid() != 0 {
		return errors.New("binding a device to vfio-pci requires root — re-run with sudo, or: vee daemon install (one-time setup)")
	}

	// Normalize address to include domain prefix.
	pciAddr := normalizePCIAddr(addr)
	base := filepath.Join("/sys/bus/pci/devices", pciAddr)

	if _, err := os.Stat(base); err != nil {
		return fmt.Errorf("PCI device %s not found: %w", pciAddr, err)
	}

	// Load vfio-pci kernel module.
	if err := exec.Command("modprobe", "vfio-pci").Run(); err != nil {
		return fmt.Errorf("modprobe vfio-pci: %w", err)
	}

	// Unbind from current driver if one is bound.
	driverLink := filepath.Join(base, "driver")
	if target, err := os.Readlink(driverLink); err == nil {
		currentDriver := filepath.Base(target)
		if currentDriver != "vfio-pci" {
			unbindPath := filepath.Join(driverLink, "unbind")
			if err := os.WriteFile(unbindPath, []byte(pciAddr), 0o200); err != nil {
				return fmt.Errorf("unbind from %s: %w", currentDriver, err)
			}
		}
	}

	// Set driver_override to vfio-pci.
	overridePath := filepath.Join(base, "driver_override")
	if err := os.WriteFile(overridePath, []byte("vfio-pci"), 0o200); err != nil {
		return fmt.Errorf("set driver_override: %w", err)
	}

	// Probe the device to trigger binding.
	probePath := "/sys/bus/pci/drivers_probe"
	if err := os.WriteFile(probePath, []byte(pciAddr), 0o200); err != nil {
		return fmt.Errorf("drivers_probe: %w", err)
	}

	// Wait briefly for the bind to take effect, then verify.
	time.Sleep(200 * time.Millisecond)
	if target, err := os.Readlink(driverLink); err == nil {
		if filepath.Base(target) == "vfio-pci" {
			return nil
		}
	}
	return fmt.Errorf("device %s did not bind to vfio-pci", pciAddr)
}

// UnbindVFIO removes a PCI device from vfio-pci and rebinds it to its original driver.
// Pass the original driver name (e.g. "amdgpu") — use "" to leave unbound.
func UnbindVFIO(addr, originalDriver string) error {
	if os.Getuid() != 0 {
		return errors.New("unbinding a device from vfio-pci requires root — re-run with sudo, or: vee daemon install (one-time setup)")
	}

	pciAddr := normalizePCIAddr(addr)
	base := filepath.Join("/sys/bus/pci/devices", pciAddr)

	driverLink := filepath.Join(base, "driver")
	if target, err := os.Readlink(driverLink); err == nil {
		currentDriver := filepath.Base(target)
		if currentDriver == "vfio-pci" {
			unbindPath := filepath.Join(driverLink, "unbind")
			if err := os.WriteFile(unbindPath, []byte(pciAddr), 0o200); err != nil {
				return fmt.Errorf("unbind from vfio-pci: %w", err)
			}
		}
	}

	// Clear driver_override.
	overridePath := filepath.Join(base, "driver_override")
	if err := os.WriteFile(overridePath, []byte("\x00"), 0o200); err != nil {
		return fmt.Errorf("clear driver_override: %w", err)
	}

	if originalDriver == "" {
		return nil
	}

	// Bind to original driver.
	bindPath := filepath.Join("/sys/bus/pci/drivers", originalDriver, "bind")
	if err := os.WriteFile(bindPath, []byte(pciAddr), 0o200); err != nil {
		return fmt.Errorf("bind to %s: %w", originalDriver, err)
	}

	return nil
}

// CurrentDriver returns the driver currently bound to a PCI device, or "" if none.
func CurrentDriver(addr string) string {
	pciAddr := normalizePCIAddr(addr)
	driverLink := filepath.Join("/sys/bus/pci/devices", pciAddr, "driver")
	target, err := os.Readlink(driverLink)
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// normalizePCIAddr ensures the address has a domain prefix (0000:).
func normalizePCIAddr(addr string) string {
	if !strings.Contains(addr, ":") || strings.Count(addr, ":") == 1 {
		return "0000:" + addr
	}
	return addr
}
