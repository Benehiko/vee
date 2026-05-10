package templates

import (
	"time"

	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

// NewPassthroughConfig returns a VMConfig for a bare-metal-style VM that
// boots directly from a raw NVMe block device.  The existing OVMF_VARS.fd
// is used as-is (no copy) by pre-setting UEFIConfig.VarsPath.
//
// nvmeDev     – host block device, e.g. "/dev/disk/by-id/nvme-CT2000P3PSSD8_…"
// ovmfVarsPath – path to an already-populated OVMF_VARS.fd
// pciAddr     – GPU PCI address for VFIO passthrough, e.g. "08:00.0"
// virtiofsMountDir – host directory to share as virtiofs tag "Games" (empty = skip)
// mac         – deterministic MAC address for the bridge NIC
func NewPassthroughConfig(p provider.Provider, name, nvmeDev, ovmfVarsPath, pciAddr, virtiofsMountDir, mac string) *vm.VMConfig {
	conf := p.Config()

	cfg := &vm.VMConfig{
		Name:     name,
		Template: "passthrough",
		Memory:   "16G",
		CPUs:     6,
		Sockets:  1,
		Cores:    3,
		Threads:  2,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:   "bridge",
			Bridge: "br0",
			Model:  "virtio-net-pci",
			MAC:    mac,
		},
		GPU: vm.GPUConfig{
			Mode:       vm.GPUPassthrough,
			PCIAddr:    pciAddr,
			AntiDetect: true,
		},
		UEFI: vm.UEFIConfig{
			Enabled:  true,
			VarsPath: ovmfVarsPath,
		},
		SPICE: &vm.SPICEConfig{
			Port:             5930,
			DisableTicketing: true,
		},
		VGA: "none",
		// virtio-gpu-pci provides a SPICE-accessible display alongside the VFIO GPU.
		ExtraDevices: []string{"virtio-gpu-pci,edid=on,xres=1920,yres=1080"},
		Disks: []vm.DiskConfig{
			{
				Path:        nvmeDev,
				Format:      "raw",
				Interface:   "virtio",
				Media:       "disk",
				Cache:       "none",
				Passthrough: true,
			},
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
