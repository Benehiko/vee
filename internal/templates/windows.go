package templates

import (
	"context"
	"time"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

// NewWindowsConfig returns a VMConfig for a Windows VM with TPM and SecureBoot.
// It downloads the Windows ISO via UUP dump if not already cached.
// The OVMF secboot firmware path must be set in provider config or overridden in vm.yaml.
// No cloud-init — Windows handles its own first-boot setup.
func NewWindowsConfig(ctx context.Context, p provider.Provider, version images.WindowsVersion, name string) (*vm.VMConfig, error) {
	conf := p.Config()

	img := images.NewWindowsImage(p, version)
	if err := img.Download(ctx); err != nil {
		return nil, err
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
		Disks: []vm.DiskConfig{
			{
				Path:       img.AbsolutePath(),
				Interface:  "virtio",
				Media:      "cdrom",
				Cache:      "none",
				Readonly:   true,
				InstallISO: true,
			},
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
