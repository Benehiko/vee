package templates

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Benehiko/vee/internal/images"
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

	// The unattend ISO lives in the VM's dir so it is regenerated per-VM and
	// removed with the VM. vmDir is <ISOCache>/../vms/<name> — but templates do
	// not know the vm dir, so stage it under the ISO cache keyed by VM name and
	// let the manager treat it as a one-shot install ISO (stripped post-install).
	unattendISO := filepath.Join(conf.ISOCachePath, "unattend-"+name+".iso")
	if err := buildUnattendISO(unattendISO, autounattend, setupScript, winfspPath); err != nil {
		return nil, fmt.Errorf("build unattend ISO: %w", err)
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
		// SPICE so the (mostly hands-free) install can still be watched and, if
		// the answer file ever needs a nudge, driven interactively.
		SPICE: &vm.SPICEConfig{
			Port:             spicePort,
			DisableTicketing: true,
		},
		Disks: []vm.DiskConfig{
			// Windows install media. IDE/SATA (not virtio) so WinPE can boot it
			// with no extra driver; the virtio system disk is handled by the
			// injected viostor driver.
			{
				Path:       img.AbsolutePath(),
				Interface:  "ide",
				Media:      "cdrom",
				Cache:      "none",
				Readonly:   true,
				InstallISO: true,
			},
			// virtio-win driver ISO (WinPE storage driver + guest tools).
			{
				Path:       virtioISO,
				Interface:  "ide",
				Media:      "cdrom",
				Cache:      "none",
				Readonly:   true,
				InstallISO: true,
			},
			// Unattend ISO (Autounattend.xml + WinFsp MSI + setup script).
			{
				Path:       unattendISO,
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
		},
		RTC:       "base=localtime,clock=host",
		CreatedAt: time.Now(),
	}, nil
}
