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

// RebindReset performs a driver unbind/rebind cycle to soft-reset a VFIO GPU
// that cannot be reset via FLR (e.g. AMD Navi31/RDNA3). The sequence is:
//
//  1. Unbind from vfio-pci
//  2. Bind to the native driver (e.g. amdgpu) — driver init acts as soft reset
//  3. Unbind from the native driver
//  4. Rebind to vfio-pci
//
// This is a best-effort workaround for GPUs without working FLR support.
// Requires write access to root-owned sysfs entries — run vee as root or via
// sudo when rebind_reset is enabled. nativeDriver is typically "amdgpu".
func RebindReset(addr, nativeDriver string) error {
	pciAddr := normalizePCIAddr(addr)
	base := filepath.Join("/sys/bus/pci/devices", pciAddr)
	driverLink := filepath.Join(base, "driver")
	overridePath := filepath.Join(base, "driver_override")

	// Step 1: unbind from vfio-pci (or whatever is currently bound).
	if target, err := os.Readlink(driverLink); err == nil {
		unbindPath := filepath.Join(driverLink, "unbind")
		if werr := os.WriteFile(unbindPath, []byte(pciAddr), 0o200); werr != nil {
			return fmt.Errorf("unbind from %s: %w", filepath.Base(target), werr)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Step 2: bind to native driver via driver_override + probe.
	if err := os.WriteFile(overridePath, []byte(nativeDriver), 0o200); err != nil {
		return fmt.Errorf("set driver_override to %s: %w", nativeDriver, err)
	}
	if err := os.WriteFile("/sys/bus/pci/drivers_probe", []byte(pciAddr), 0o200); err != nil {
		// Clear override so we don't leave the device in a weird state.
		_ = os.WriteFile(overridePath, []byte("\x00"), 0o200)
		return fmt.Errorf("probe for %s bind: %w", nativeDriver, err)
	}

	// Wait for native driver to fully initialize (GPU soft-reset happens here).
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if filepath.Base(func() string { t, _ := os.Readlink(driverLink); return t }()) == nativeDriver {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if filepath.Base(func() string { t, _ := os.Readlink(driverLink); return t }()) != nativeDriver {
		return fmt.Errorf("device %s did not bind to %s within timeout", pciAddr, nativeDriver)
	}
	// Hold briefly so the driver fully initializes.
	time.Sleep(500 * time.Millisecond)

	// Step 3: unbind native driver.
	unbindNative := filepath.Join(driverLink, "unbind")
	if err := os.WriteFile(unbindNative, []byte(pciAddr), 0o200); err != nil {
		return fmt.Errorf("unbind from %s: %w", nativeDriver, err)
	}
	time.Sleep(200 * time.Millisecond)

	// Step 4: rebind to vfio-pci.
	if err := os.WriteFile(overridePath, []byte("vfio-pci"), 0o200); err != nil {
		return fmt.Errorf("set driver_override to vfio-pci: %w", err)
	}
	if err := os.WriteFile("/sys/bus/pci/drivers_probe", []byte(pciAddr), 0o200); err != nil {
		return fmt.Errorf("probe for vfio-pci bind: %w", err)
	}

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if filepath.Base(func() string { t, _ := os.Readlink(driverLink); return t }()) == "vfio-pci" {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("device %s did not rebind to vfio-pci", pciAddr)
}

// normalizePCIAddr ensures the address has a domain prefix (0000:).
func normalizePCIAddr(addr string) string {
	if !strings.Contains(addr, ":") || strings.Count(addr, ":") == 1 {
		return "0000:" + addr
	}
	return addr
}
