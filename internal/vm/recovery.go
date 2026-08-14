package vm

import (
	"fmt"

	"github.com/Benehiko/vee/internal/backend"
)

// RecoveryMode says how — or whether — a guest can be booted into its
// recovery/rescue environment at launch (issue #134). "Recovery" is a
// different mechanism per guest OS, so the uniform `vee start --recovery`
// flag dispatches through this plan instead of a single switch:
//
//   - macOS on vz: a hypervisor start option (recoveryOS)
//   - Linux on vz with a direct-kernel boot: a kernel-cmdline injection
//   - everything else (EFI/GRUB whole-disk boots, Windows): not expressible
//     at launch — the start proceeds normally with a warning, never a silent
//     success
type RecoveryMode int

const (
	// RecoveryUnsupported: vee has no launch-time hook for this guest; the
	// plan's message says why and what to do instead.
	RecoveryUnsupported RecoveryMode = iota
	// RecoveryMacOS boots a vz macOS guest into recoveryOS via
	// VZMacOSVirtualMachineStartOptions.startUpFromMacOSRecovery.
	RecoveryMacOS
	// RecoveryLinuxKernel boots a vz direct-kernel Linux guest into the
	// systemd rescue target by appending to the kernel command line for this
	// start only.
	RecoveryLinuxKernel
)

// linuxRescueCmdline is appended to a direct-kernel Linux guest's command
// line for a recovery start: single-user maintenance shell, no network.
const linuxRescueCmdline = "systemd.unit=rescue.target"

// RecoveryPlan reports how `--recovery` applies to cfg. The returned message
// is user guidance: for a supported mode, what to expect and how to reach the
// guest (recovery environments have no SSH); for RecoveryUnsupported, why the
// start will be a normal boot and how to reach recovery instead.
func RecoveryPlan(cfg *VMConfig) (RecoveryMode, string) {
	if cfg.BackendName() == backend.VZ {
		if cfg.MacOS != nil {
			return RecoveryMacOS, fmt.Sprintf(
				"booting into recoveryOS — SSH is unavailable there; open the guest's display with `vee view %s`", cfg.Name)
		}
		if cfg.Kernel != "" {
			return RecoveryLinuxKernel, fmt.Sprintf(
				"booting into the systemd rescue target (%s) — networking and SSH are down there; the maintenance shell is on the guest console: `vee logs %s` tails it (serial.log), or start with --foreground to stream it live", linuxRescueCmdline, cfg.Name)
		}
		return RecoveryUnsupported, fmt.Sprintf(
			"this guest boots via EFI from its own disk, so vee has no kernel-cmdline hook to request rescue mode — booting normally; use the bootloader's menu instead, or add rescue parameters from inside the guest (`vee ssh %s`)", cfg.Name)
	}
	if cfg.Template == "windows" {
		return RecoveryUnsupported, fmt.Sprintf(
			"Windows recovery (WinRE) is armed from inside the guest, not at launch — booting normally; run `reagentc /boottore` in the guest (e.g. `vee ssh %s`), then reboot: the next boot lands in WinRE", cfg.Name)
	}
	return RecoveryUnsupported, fmt.Sprintf(
		"this guest boots through the disk's own bootloader (GRUB/EFI), so vee has no kernel-cmdline hook to request rescue mode — booting normally; use the bootloader's menu from the display/serial console instead, or add rescue parameters from inside the guest (`vee ssh %s`)", cfg.Name)
}
