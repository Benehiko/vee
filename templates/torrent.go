package templates

import (
	"time"

	"github.com/Benehiko/vee/cloudinit"
	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/vm"
)

// NewTorrentConfig returns a VMConfig for a lightweight torrent VM with SPICE display.
// spicePort defaults to 5934 if 0.
func NewTorrentConfig(p provider.Provider, name string, spicePort int) *vm.VMConfig {
	conf := p.Config()
	if spicePort == 0 {
		spicePort = 5934
	}

	return &vm.VMConfig{
		Name:     name,
		Template: "torrent",
		Memory:   "4G",
		CPUs:     2,
		Sockets:  1,
		Cores:    2,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:  "user",
			Model: "virtio-net-pci",
		},
		GPU: vm.GPUConfig{Mode: vm.GPUNone},
		UEFI: vm.UEFIConfig{
			Enabled: true,
		},
		SPICE: &vm.SPICEConfig{
			Port:             spicePort,
			DisableTicketing: true,
		},
		CloudInit: &vm.CloudInitConfig{
			Hostname: name,
			User:     "vee",
			Packages: cloudinit.PackagesFor(cloudinit.Ubuntu, cloudinit.CategoryTorrent),
			RunCmds: []string{
				"systemctl enable --now qbittorrent-nox@vee",
				"ufw allow 8080/tcp",
				"ufw --force enable",
			},
		},
		CreatedAt: time.Now(),
	}
}
