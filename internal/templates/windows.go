package templates

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/platform"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

// NewWindowsConfig returns a VMConfig for a Windows VM with TPM and SecureBoot.
//
// The install is unattended: alongside the UUP-dump Windows ISO it attaches the
// virtio-win driver ISO and a generated "unattend" ISO (Autounattend.xml +
// WinFsp MSI + guest setup script). The answer file injects the viostor driver
// in WinPE so Setup sees the virtio system disk, wipes and partitions it,
// creates a local admin, skips OOBE, and runs the guest setup script at first
// logon to install WinFsp + virtio-win guest tools (so a virtiofs share mounts
// as a drive). See windows_unattend.go.
//
// virtiofsTag is the mount tag of the virtiofs share the caller wired via
// --virtiofs-dir (empty if none); it is only used to log guidance in the guest
// setup script. spicePort is the SPICE port (0 = auto-assign) so the install
// can be watched via `vee view` / `vee tunnel`.
//
// The OVMF secboot firmware path must be set in provider config or overridden
// in vm.yaml.
func NewWindowsConfig(ctx context.Context, p provider.Provider, version images.WindowsVersion, name, virtiofsTag string, spicePort int) (*vm.VMConfig, error) {
	conf := p.Config()

	// The Windows image pipeline (UUP dump) and x86 Secure Boot OVMF are
	// x86_64-only. Windows-on-ARM has no vee image pipeline, and even when run,
	// Windows guests get no virtio-gpu 3D acceleration (no guest driver) and
	// VFIO passthrough is unavailable on macOS. Refuse clearly on aarch64.
	if platform.HostArch() == "arm64" {
		return nil, fmt.Errorf("the windows template is x86_64-only; Windows-on-ARM has no vee image pipeline, " +
			"and Windows guests get no GPU 3D acceleration on a macOS host (no virtio-gpu driver, no VFIO)")
	}

	img := images.NewWindowsImage(p, version)
	if err := img.Download(ctx); err != nil {
		return nil, err
	}

	// virtio-win driver ISO — provides viostor (WinPE storage), NetKVM, and the
	// guest-tools installer (viofs) the unattend flow needs.
	virtioISO, err := ensureCachedDownload(ctx, p, virtioWinURL, virtioWinISO)
	if err != nil {
		return nil, fmt.Errorf("virtio-win driver ISO: %w", err)
	}

	// WinFsp MSI — the virtiofs client is a WinFsp filesystem, so WinFsp must be
	// installed before the VirtioFS service can start. Baked into the unattend
	// ISO and installed by the guest setup script.
	winfspPath, err := ensureCachedDownload(ctx, p, winfspURL, winfspMSI)
	if err != nil {
		return nil, fmt.Errorf("WinFsp MSI: %w", err)
	}

	driverDir, ok := virtioWinDriverDir[version]
	if !ok {
		driverDir = "w11"
	}
	autounattend := autounattendXML(version, driverDir, virtiofsTag)
	setupScript := guestSetupPS1(virtiofsTag)

	// The install media must not show "Press any key to boot from CD" — that
	// prompt times out on an unattended boot and the firmware falls through to a
	// PXE loop, and QMP key injection does not reach it. Rebuild the media with
	// the prompt-free EFI boot image. Falls back to the original ISO if the tool
	// (oscdimg) is unavailable.
	installISOPath, err := ensureNoPromptISO(ctx, p, img.AbsolutePath())
	if err != nil {
		return nil, fmt.Errorf("prepare no-prompt install media: %w", err)
	}

	// One "extras" ISO: virtio-win drivers + Autounattend.xml + WinFsp + the
	// first-logon setup script. A single ISO (not three) because q35 reboot-
	// loops WinPE with more than two optical drives. Staged per-VM under the ISO
	// cache and treated as a one-shot install ISO (stripped post-install).
	extrasISO := filepath.Join(conf.ISOCachePath, "win-extras-"+name+".iso")
	if err := buildExtrasISO(ctx, p, extrasISO, virtioISO, autounattend, setupScript, winfspPath); err != nil {
		return nil, fmt.Errorf("build extras ISO: %w", err)
	}

	// Secboot OVMF for Windows 11 Secure Boot requirement.
	// On Arch: /usr/share/OVMF/x64/OVMF_CODE.secboot.4m.fd
	secbootCode := conf.OVMFSecbootCodePath
	if secbootCode == "" {
		secbootCode = conf.OVMFCodePath
	}

	return &vm.VMConfig{
		Name:     name,
		Template: "windows",
		Memory:   "24G",
		CPUs:     4,
		Sockets:  1,
		Cores:    4,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		// Hyper-V enlightenments. Without them Windows' bootloader / WinPE
		// triple-faults and resets in a loop before Setup ever draws (a dark
		// blue screen that immediately reboots), so the install never starts.
		CPUFlags: []string{
			"hv-relaxed=on",
			"hv-vapic=on",
			"hv-time=on",
			"hv-spinlocks=0x1fff",
		},
		// SMM is required for the secboot OVMF firmware. The secure-pflash
		// -global arms SMM-protected Secure Boot; both are needed for Windows 11
		// to boot the installer here.
		MachineType: "q35,smm=on",
		Globals: []string{
			"driver=cfi.pflash01,property=secure,value=on",
		},
		NIC: vm.NICConfig{
			Mode:  "user",
			Model: "virtio-net-pci",
		},
		GPU: vm.GPUConfig{Mode: vm.GPUNone},
		UEFI: vm.UEFIConfig{
			Enabled:  true,
			CodePath: secbootCode,
		},
		TPM: &vm.TPMConfig{
			Enabled: true,
		},
		// USB HID: a USB keyboard + tablet (absolute pointer) on an xHCI
		// controller. Windows guests expect USB input under SPICE, and — unlike
		// the bare PS/2 keyboard — the USB keyboard is the input device OVMF and
		// the Windows boot manager read during early boot, so QMP send-key
		// events reach prompts like "Press any key to boot from CD".
		ExtraDevices: []string{
			"qemu-xhci,id=xhci",
			"usb-kbd,bus=xhci.0",
			"usb-tablet,bus=xhci.0",
		},
		// SPICE so the (mostly hands-free) install can still be watched and, if
		// the answer file ever needs a nudge, driven interactively.
		SPICE: &vm.SPICEConfig{
			Port:             spicePort,
			DisableTicketing: true,
		},
		Disks: []vm.DiskConfig{
			// Windows install media (no-prompt). IDE (not virtio) so WinPE can
			// boot it with no extra driver; the virtio system disk is handled by
			// the injected viostor driver. Exactly two optical drives — q35
			// reboot-loops WinPE with three or more.
			{
				Path:       installISOPath,
				Interface:  "ide",
				Media:      "cdrom",
				Cache:      "none",
				Readonly:   true,
				InstallISO: true,
			},
			// Extras ISO: virtio-win drivers + Autounattend.xml + WinFsp + setup
			// script, all on one volume.
			{
				Path:       extrasISO,
				Interface:  "ide",
				Media:      "cdrom",
				Cache:      "none",
				Readonly:   true,
				InstallISO: true,
			},
			// System disk (virtio; driver injected during Setup).
			{
				Path:      "",
				Size:      conf.DefaultDiskSize,
				Format:    "qcow2",
				Interface: "virtio",
				Media:     "disk",
				Cache:     "writeback",
			},
			// Scratch disk (virtio; viostor drvloaded in WinPE by startnet.cmd).
			// Windows 11 24H2 "ConX" Setup writes its $WINDOWS.~BT working tree
			// onto the drive setup.exe runs from; the install DVD is read-only,
			// so Setup must be launched from writable media or it fails with
			// OneSettings 0x800702E7 (issue #17). startnet.cmd formats this disk,
			// copies the DVD tree onto it, and runs setup.exe from here. Must
			// stay LAST in the array so it enumerates as virtio disk 1 (OS disk =
			// disk 0); startnet selects the highest disk index as the scratch and
			// never touches the OS disk. Sized 8G to hold the ~3.7 GB DVD copy
			// plus Setup's $WINDOWS.~BT working set. Thrown away after install.
			{
				Path:      "",
				Size:      "8G",
				Format:    "qcow2",
				Interface: "virtio",
				Media:     "disk",
				Cache:     "writeback",
				Scratch:   true,
			},
		},
		RTC:       "base=localtime,clock=host",
		CreatedAt: time.Now(),
	}, nil
}
