//go:build darwin

package qemu

import "go.uber.org/zap"

// applyVFIOLimits is a no-op on macOS. VFIO PCI passthrough is a Linux kernel
// feature with no Hypervisor.framework equivalent, so no guest RAM is ever
// DMA-mapped into an IOMMU and there is no RLIMIT_MEMLOCK to raise. Passthrough
// requests are rejected earlier (see the VM manager), but warn defensively if a
// VFIO device somehow reached this point.
func (q *BaseMachine) applyVFIOLimits(_ int) error {
	if len(q.vfioDevices) > 0 {
		q.provider.Logger().Warn("VFIO devices configured but VFIO is unsupported on macOS — ignoring",
			zap.String("machine", q.name),
			zap.Int("vfio_devices", len(q.vfioDevices)),
		)
	}
	return nil
}

// applyCPUPinning is a no-op on macOS. Pinning relies on taskset and
// /proc/<pid>/task, neither of which exists on Darwin; Hypervisor.framework
// does not expose stable per-vCPU host threads to pin. Warn if pinning was
// requested so the user understands it is being ignored.
func (q *BaseMachine) applyCPUPinning(pid int) {
	if len(q.cpuPinning) > 0 {
		q.provider.Logger().Warn("CPU pinning requested but is unsupported on macOS — ignoring",
			zap.String("machine", q.name),
			zap.Int("pid", pid),
		)
	}
}
