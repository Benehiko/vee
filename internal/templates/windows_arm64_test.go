package templates

import (
	"strings"
	"testing"

	"github.com/Benehiko/vee/provider"
)

// TestWindowsARM64ConfigShape pins the aarch64 Windows VM's platform-forced
// choices. Each assertion guards a decision that took research to get right,
// so a casual edit must not silently regress it.
func TestWindowsARM64ConfigShape(t *testing.T) {
	conf := &provider.Config{DefaultDiskSize: "64G"}
	cfg := windowsARM64Config(conf, "win", "/vms/win", "/iso/install.iso", "/iso/extras.iso")

	// The virt board has no SMM and vee's aarch64 edk2 has no Secure Boot
	// variant; the default machine type (empty = virt) must be used.
	if cfg.MachineType != "" {
		t.Errorf("machine type should be the virt default, got %q", cfg.MachineType)
	}
	// Hyper-V enlightenments are x86-only; the manager would drop them with a
	// warning, so the template must not set any.
	if len(cfg.CPUFlags) != 0 {
		t.Errorf("aarch64 config carries x86 CPU flags: %v", cfg.CPUFlags)
	}
	// Windows ARM64 cannot initialize QEMU's sysbus tpm-tis-device (QEMU
	// issue #830); the LabConfig bypass covers the Windows 11 check instead.
	if cfg.TPM != nil {
		t.Error("aarch64 config must not carry a TPM (Windows ARM64 cannot bind tpm-tis-device)")
	}
	// The install renders on ramfb via the host QEMU window; SPICE is not
	// part of the arm64 shape.
	if cfg.SPICE != nil {
		t.Error("aarch64 config must not configure SPICE")
	}
	if !cfg.UEFI.Enabled {
		t.Error("aarch64 virt has no BIOS; UEFI must be enabled")
	}

	// ramfb is the one display Windows ARM64 drives out of the box, and the
	// USB input devices need an xhci controller — which the USB install media
	// also depends on.
	devices := strings.Join(cfg.ExtraDevices, " ")
	for _, want := range []string{"ramfb", "qemu-xhci", "usb-kbd", "usb-tablet"} {
		if !strings.Contains(devices, want) {
			t.Errorf("extra devices missing %q: %v", want, cfg.ExtraDevices)
		}
	}

	if len(cfg.Disks) != 4 {
		t.Fatalf("expected 4 disks (install, extras, os, scratch), got %d", len(cfg.Disks))
	}
	assertDisk := func(i int, iface, media string, installISO, scratch bool) {
		t.Helper()
		d := cfg.Disks[i]
		if d.Interface != iface || d.Media != media {
			t.Errorf("disk %d: got %s/%s, want %s/%s", i, d.Interface, d.Media, iface, media)
		}
		if d.InstallISO != installISO {
			t.Errorf("disk %d: InstallISO = %v, want %v", i, d.InstallISO, installISO)
		}
		if d.Scratch != scratch {
			t.Errorf("disk %d: Scratch = %v, want %v", i, d.Scratch, scratch)
		}
	}
	// CDROMs ride USB (virt has no IDE); disks ride NVMe (inbox stornvme —
	// no driver injection needed for Setup to see them).
	assertDisk(0, "usb", "cdrom", true, false)
	assertDisk(1, "usb", "cdrom", true, false)
	assertDisk(2, "nvme", "disk", false, false)
	// The scratch disk must stay LAST: startnet.cmd picks the highest disk
	// index as the 24H2 scratch and must never select the OS disk.
	assertDisk(3, "nvme", "disk", false, true)

	// No bootindex anywhere: fresh edk2 vars fall through the empty NVMe to
	// the install media, and once Setup writes the Windows Boot Manager entry
	// the disk must win — a pinned cdrom bootindex would reboot-loop Setup.
	for i, d := range cfg.Disks {
		if d.BootIndex != 0 {
			t.Errorf("disk %d: unexpected bootindex %d", i, d.BootIndex)
		}
	}

	if cfg.NIC.Model != "virtio-net-pci" {
		t.Errorf("NIC model %q; NetKVM's ARM64 build targets virtio-net-pci", cfg.NIC.Model)
	}
	if cfg.Disks[2].Size != "64G" {
		t.Errorf("os disk size %q, want the provider default 64G", cfg.Disks[2].Size)
	}

	// SSH must be turnkey: the unattend flow enables sshd + authorized keys,
	// so the config must carry the forwarded port that makes them reachable.
	if cfg.SSHPort <= 0 {
		t.Error("aarch64 config has no forwarded SSH port; the guest would be unreachable on user-mode NAT")
	}
	// ...and the unattend account, or `vee ssh` falls back to the local host
	// username (Windows has no cloud-init user to resolve) and auth fails.
	if cfg.SSHUser != winAdminUser {
		t.Errorf("ssh user %q, want the unattend account %q", cfg.SSHUser, winAdminUser)
	}
	// qemu-ga rides the vioserial driver, which is test-signed on ARM64 and
	// cannot load — attaching the QGA channel would just be a dead device.
	if cfg.GuestAgent {
		t.Error("aarch64 config must not attach a QGA channel (vioserial is test-signed on ARM64)")
	}
}

// TestWindowsARM64InterfaceNames pins the interface strings in the arm64
// VMConfig to the ones internal/qemu understands (InterfaceUSB, InterfaceNVMe).
func TestWindowsARM64InterfaceNames(t *testing.T) {
	conf := &provider.Config{DefaultDiskSize: "64G"}
	cfg := windowsARM64Config(conf, "win", "/vms/win", "/iso/install.iso", "/iso/extras.iso")
	valid := map[string]bool{"usb": true, "nvme": true}
	for i, d := range cfg.Disks {
		if !valid[d.Interface] {
			t.Errorf("disk %d: interface %q is not one of the arm64 shapes (usb, nvme)", i, d.Interface)
		}
	}
}
