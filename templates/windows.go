package templates

import (
	"time"

	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/vm"
)

// NewWindowsConfig returns a VMConfig for a Windows 11 VM with TPM and SecureBoot.
// The OVMF secboot firmware path must be set in provider config or overridden in vm.yaml.
// No cloud-init — Windows handles its own first-boot setup.
func NewWindowsConfig(p provider.Provider, name string) *vm.VMConfig {
	conf := p.Config()

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
		CreatedAt: time.Now(),
	}
}
