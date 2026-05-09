package gpu

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PowerState represents the PCI power management state of a device.
type PowerState string

const (
	PowerStateD0      PowerState = "D0"     // fully on
	PowerStateD1      PowerState = "D1"     // intermediate
	PowerStateD2      PowerState = "D2"     // intermediate
	PowerStateD3hot   PowerState = "D3hot"  // off, power still applied
	PowerStateD3cold  PowerState = "D3cold" // off, no power — requires cold reset to recover
	PowerStateUnknown PowerState = "unknown"
)

// DeviceState holds observed power and runtime state for a PCI device.
type DeviceState struct {
	PCIAddr        string
	PowerState     PowerState
	RuntimeStatus  string // "active", "suspended", "error", etc.
	ResetAvailable bool
	ResetMethod    string
}

// ReadDeviceState reads power and reset state from sysfs for the given PCI address.
func ReadDeviceState(addr string) DeviceState {
	pciAddr := normalizePCIAddr(addr)
	base := filepath.Join("/sys/bus/pci/devices", pciAddr)

	s := DeviceState{PCIAddr: pciAddr}

	if raw := readSysFile(filepath.Join(base, "power_state")); raw != "" {
		s.PowerState = PowerState(raw)
	} else {
		s.PowerState = PowerStateUnknown
	}

	s.RuntimeStatus = readSysFile(filepath.Join(base, "power", "runtime_status"))

	if _, err := os.Stat(filepath.Join(base, "reset")); err == nil {
		s.ResetAvailable = true
	}
	s.ResetMethod = readSysFile(filepath.Join(base, "reset_method"))

	return s
}

// NeedsReset reports whether the device is in a state that requires a reset
// before it can be safely opened by VFIO.
func (s *DeviceState) NeedsReset() bool {
	switch s.PowerState {
	case PowerStateD3cold:
		return true
	}
	switch s.RuntimeStatus {
	case "error", "suspended":
		// "suspended" under vfio-pci after an unclean exit means the device
		// was not properly resumed by the previous QEMU instance.
		return s.PowerState == PowerStateD3cold || strings.Contains(string(s.PowerState), "D3")
	}
	return false
}

// Reset triggers a PCI function-level reset via sysfs.
// Requires write access to /sys/bus/pci/devices/<addr>/reset (root).
// Blocks until the device acknowledges the reset or timeout elapses.
func Reset(addr string) error {
	pciAddr := normalizePCIAddr(addr)
	resetPath := filepath.Join("/sys/bus/pci/devices", pciAddr, "reset")

	if _, err := os.Stat(resetPath); err != nil {
		return fmt.Errorf("device %s does not support sysfs reset (no reset file)", pciAddr)
	}

	if err := os.WriteFile(resetPath, []byte("1"), 0o200); err != nil {
		return fmt.Errorf("reset %s: %w", pciAddr, err)
	}

	// Poll until the device leaves D3cold or timeout.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s := ReadDeviceState(pciAddr)
		if s.PowerState != PowerStateD3cold && s.RuntimeStatus != "suspended" {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Read final state to include in the error.
	s := ReadDeviceState(pciAddr)
	return fmt.Errorf("device %s still in %s/%s after reset — cold reboot may be required",
		pciAddr, s.PowerState, s.RuntimeStatus)
}

// EnsureReady checks the device state and attempts a reset if needed.
// Returns the observed state and any error from the reset attempt.
// If reset is not available (no root, no sysfs file), returns the state
// and a descriptive error without attempting.
func EnsureReady(addr string) (DeviceState, error) {
	s := ReadDeviceState(addr)
	if !s.NeedsReset() {
		return s, nil
	}

	if !s.ResetAvailable {
		return s, fmt.Errorf(
			"device %s is in %s/%s and has no sysfs reset — cold reboot required",
			s.PCIAddr, s.PowerState, s.RuntimeStatus)
	}

	if err := Reset(addr); err != nil {
		return ReadDeviceState(addr), fmt.Errorf("reset failed: %w — cold reboot may be required", err)
	}

	return ReadDeviceState(addr), nil
}
