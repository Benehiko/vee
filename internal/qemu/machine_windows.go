//go:build windows

package qemu

import "go.uber.org/zap"

// qemuEnv returns nil so the QEMU child inherits the parent environment. On
// Windows the bundle's DLLs sit next to the .exe and are found via the default
// DLL search path; the Linux LD_LIBRARY_PATH libaio fix does not apply here.
// See issue #40.
func qemuEnv(_ string) []string { return nil }

// applyVFIOLimits is a no-op on Windows. VFIO PCI passthrough is a Linux kernel
// feature with no Windows Hypervisor Platform equivalent, so there is no guest
// RAM DMA-mapped into an IOMMU and no RLIMIT_MEMLOCK to raise. Passthrough
// requests are rejected earlier (see the VM manager), but warn defensively if a
// VFIO device somehow reached this point.
func (q *BaseMachine) applyVFIOLimits(_ int) error {
	if len(q.vfioDevices) > 0 {
		q.provider.Logger().Warn("VFIO devices configured but VFIO is unsupported on Windows — ignoring",
			zap.String("machine", q.name),
			zap.Int("vfio_devices", len(q.vfioDevices)),
		)
	}
	return nil
}

// applyCPUPinning is a no-op on Windows. Pinning relies on taskset and
// /proc/<pid>/task, neither of which exists on Windows. Warn if pinning was
// requested so the user understands it is being ignored.
func (q *BaseMachine) applyCPUPinning(pid int) {
	if len(q.cpuPinning) > 0 {
		q.provider.Logger().Warn("CPU pinning requested but is unsupported on Windows — ignoring",
			zap.String("machine", q.name),
			zap.Int("pid", pid),
		)
	}
}
