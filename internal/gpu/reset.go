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
//
// D3cold is the only state that truly blocks VFIO — the device has no power
// and cannot respond to any transactions. D3hot/suspended is handled by
// vfio-pci itself during device open (it triggers a runtime resume).
//
// Empirically, attempting to launch QEMU against a D3cold device — even when
// runtime_status reports "active" — aborts in vfio_pci_interrupt_setup
// during MSI/MSI-X programming, because the device cannot acknowledge the
// MSI capability access. So we treat any D3cold reading as unsafe, regardless
// of runtime_status.
func (s *DeviceState) NeedsReset() bool {
	return s.PowerState == PowerStateD3cold
}

// NeedsAttention reports softer conditions worth logging as warnings but that
// do not require a reset — e.g. D3hot/suspended after an unclean exit.
func (s *DeviceState) NeedsAttention() bool {
	if s.NeedsReset() {
		return false
	}
	if s.RuntimeStatus == "suspended" && strings.Contains(string(s.PowerState), "D3") {
		return true
	}
	return s.RuntimeStatus == "error"
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
		if !s.NeedsReset() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Read final state to include in the error.
	s := ReadDeviceState(pciAddr)
	return fmt.Errorf("device %s still in %s/%s after reset — cold reboot may be required",
		pciAddr, s.PowerState, s.RuntimeStatus)
}

// WakeDevice forces the PCI device to D0 by writing "on" to its power/control
// sysfs knob, then polling until power_state is no longer D3cold/suspended.
// This does not require root — only membership in the vfio group.
func WakeDevice(addr string) error {
	pciAddr := normalizePCIAddr(addr)
	controlPath := filepath.Join("/sys/bus/pci/devices", pciAddr, "power", "control")

	if err := os.WriteFile(controlPath, []byte("on"), 0o600); err != nil {
		return fmt.Errorf("write power/control for %s: %w", pciAddr, err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s := ReadDeviceState(pciAddr)
		if !s.NeedsReset() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	s := ReadDeviceState(pciAddr)
	return fmt.Errorf("device %s still in %s/%s after wake — cold reboot may be required",
		pciAddr, s.PowerState, s.RuntimeStatus)
}

// EnsureReady checks the device state and attempts a wake+reset if needed.
// Returns the observed state and any error from the attempt.
func EnsureReady(addr string) (DeviceState, error) {
	s := ReadDeviceState(addr)
	if !s.NeedsReset() {
		return s, nil
	}

	// Try writing power/control=on first — works without root for vfio group members.
	if wakeErr := WakeDevice(addr); wakeErr == nil {
		s = ReadDeviceState(addr)
		if !s.NeedsReset() {
			return s, nil
		}
	}

	// Fall back to sysfs reset (requires write access to /sys/.../reset).
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
