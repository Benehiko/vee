// Package platform centralizes host-OS and host-architecture decisions so the
// rest of vee can stay free of scattered runtime.GOOS/GOARCH checks.
//
// vee historically targeted Linux/KVM exclusively. To run on macOS (Apple
// Silicon) the launcher must pick a different accelerator (HVF), guest
// architecture (aarch64), machine type (virt), QEMU binary, and display
// backend, and it must avoid Linux-only subsystems (VFIO, vhost-vsock,
// virtiofsd, bridge helper, CPU pinning via /proc+taskset). The helpers here
// describe those host-derived defaults and capability gates in one place.
package platform

import "runtime"

// HostOS returns the host operating system ("linux", "darwin", ...).
func HostOS() string { return runtime.GOOS }

// HostArch returns the host architecture ("amd64", "arm64", ...).
func HostArch() string { return runtime.GOARCH }

// IsMacOS reports whether vee is running on a macOS host.
func IsMacOS() bool { return runtime.GOOS == "darwin" }

// IsLinux reports whether vee is running on a Linux host.
func IsLinux() bool { return runtime.GOOS == "linux" }

// IsWindows reports whether vee is running on a Windows host.
func IsWindows() bool { return runtime.GOOS == "windows" }

// DefaultGuestArch returns the QEMU guest architecture that runs natively
// (hardware-accelerated) on this host — i.e. the host's own architecture
// expressed in QEMU's naming. Cross-architecture guests are possible but fall
// back to TCG emulation, so this is the sensible default for new VMs.
func DefaultGuestArch() string {
	return GuestArchForHostArch(runtime.GOARCH)
}

// GuestArchForHostArch maps a Go GOARCH value to the QEMU guest arch string.
func GuestArchForHostArch(goarch string) string {
	switch goarch {
	case "arm64":
		return "aarch64"
	case "amd64":
		return "x86_64"
	default:
		// Unknown hosts fall back to x86_64 (TCG). Callers should warn.
		return "x86_64"
	}
}

// DefaultAccelerator returns the native hypervisor accelerator for this host:
// "kvm" on Linux, "hvf" (Hypervisor.framework) on macOS, "whpx" (Windows
// Hypervisor Platform) on Windows, "tcg" otherwise. WHPX requires the "Windows
// Hypervisor Platform" (and Hyper-V) features to be enabled on the host.
func DefaultAccelerator() string {
	switch runtime.GOOS {
	case "darwin":
		return "hvf"
	case "linux":
		return "kvm"
	case "windows":
		return "whpx"
	default:
		return "tcg"
	}
}

// MachineTypeForArch returns the default QEMU machine type for a guest arch.
// aarch64 uses the generic "virt" board; x86_64 uses "q35".
func MachineTypeForArch(arch string) string {
	switch arch {
	case "aarch64", "arm64":
		return "virt"
	default:
		return "q35"
	}
}

// DefaultMachineType returns the machine type for this host's native guest arch.
func DefaultMachineType() string {
	return MachineTypeForArch(DefaultGuestArch())
}

// QemuBinaryName returns the qemu-system binary name for a guest arch, e.g.
// "qemu-system-aarch64" or "qemu-system-x86_64".
func QemuBinaryName(arch string) string {
	return "qemu-system-" + arch
}

// DefaultQemuBinaryName returns the qemu-system binary name for this host's
// native guest arch.
func DefaultQemuBinaryName() string {
	return QemuBinaryName(DefaultGuestArch())
}

// Capability gates. These describe whether a Linux-kernel-specific subsystem is
// available on the current host. Callers should degrade gracefully (clear error
// or fallback) rather than emit QEMU arguments that cannot work.

// SupportsVFIO reports whether VFIO PCI passthrough is possible. It is a Linux
// kernel feature with no macOS equivalent.
func SupportsVFIO() bool { return runtime.GOOS == "linux" }

// SupportsVsock reports whether vhost-vsock-pci is available. vhost is Linux
// only; on macOS, SSH-over-vsock must fall back to user-mode port forwarding.
func SupportsVsock() bool { return runtime.GOOS == "linux" }

// SupportsVirtiofsd reports whether the virtiofsd daemon is available. The
// reference virtiofsd is Linux only.
func SupportsVirtiofsd() bool { return runtime.GOOS == "linux" }

// SupportsBridgeNetworking reports whether QEMU bridge networking (via the
// setuid qemu-bridge-helper) is available. macOS uses user-mode NAT instead.
func SupportsBridgeNetworking() bool { return runtime.GOOS == "linux" }

// SupportsCPUPinning reports whether vCPU-thread pinning (taskset + /proc) is
// available. Linux only.
func SupportsCPUPinning() bool { return runtime.GOOS == "linux" }

// SupportsSwTPM reports whether the swtpm software-TPM daemon path is wired up.
// Currently Linux only.
func SupportsSwTPM() bool { return runtime.GOOS == "linux" }
