package templates

import (
	"time"

	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/vm"
)

// NewGamingConfig returns a VMConfig for a GPU passthrough gaming VM.
// pciAddr is the host PCI address of the GPU to pass through (e.g. "08:00.0").
// Set virtiofsMountDir to share a host games directory (leave empty to skip).
func NewGamingConfig(p provider.Provider, name, pciAddr, virtiofsMountDir string) *vm.VMConfig {
	conf := p.Config()

	cfg := &vm.VMConfig{
		Name:     name,
		Template: "gaming",
		Memory:   "16G",
		CPUs:     6,
		Sockets:  1,
		Cores:    6,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:   "bridge",
			Bridge: "br0",
			Model:  "virtio-net-pci",
		},
		GPU: vm.GPUConfig{
			Mode:       vm.GPUPassthrough,
			PCIAddr:    pciAddr,
			AntiDetect: true,
		},
		UEFI: vm.UEFIConfig{
			Enabled: true,
		},
		CreatedAt: time.Now(),
	}

	if virtiofsMountDir != "" {
		cfg.VirtiofsMounts = []vm.VirtiofsMount{
			{
				SharedDir: virtiofsMountDir,
				Tag:       "Games",
			},
		}
	}

	return cfg
}
