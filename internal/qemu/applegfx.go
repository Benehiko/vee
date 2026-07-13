package qemu

// AppleGFXDevice returns the -device value for Apple's ParavirtualizedGraphics
// (PVG) device, which accelerates a macOS guest's display through the host's
// ParavirtualizedGraphics.framework. It requires QEMU >= 10.0, a macOS host
// with a Metal-capable GPU, the HVF accelerator, and a QEMU binary code-signed
// with com.apple.security.hypervisor (plus com.apple.security.virtualization for
// the vmapple machine).
//
//   - aarch64 macOS guests use apple-gfx-mmio (the device wired up by the
//     vmapple machine, mirroring Apple's Virtualization.framework).
//   - x86_64 macOS guests use apple-gfx-pci.
//
// apple-gfx accelerates macOS guests only — there is no Linux or Windows guest
// driver.
func AppleGFXDevice(arch string) string {
	if arch == "aarch64" || arch == "arm64" {
		return "apple-gfx-mmio"
	}
	return "apple-gfx-pci"
}

// VMAppleMachineType is the QEMU machine type that approximates Apple's
// Virtualization.framework configuration for aarch64 macOS guests. It pulls in
// apple-gfx-mmio and requires AVPBooter from the host Virtualization.framework
// as its -bios.
const VMAppleMachineType = "vmapple"
