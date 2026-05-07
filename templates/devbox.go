package templates

import (
	"time"

	"github.com/Benehiko/vee/cloudinit"
	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/vm"
)

// NewDevboxConfig returns a VMConfig for a developer workstation VM.
// sshKeys are injected into the default user's authorized_keys via cloud-init.
func NewDevboxConfig(p provider.Provider, name string, sshKeys []string) *vm.VMConfig {
	conf := p.Config()

	pkgs := cloudinit.PackagesFor(cloudinit.Ubuntu, cloudinit.CategoryDevbox)

	return &vm.VMConfig{
		Name:     name,
		Template: "devbox",
		Memory:   "8G",
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
			Enabled: true,
		},
		CloudInit: &vm.CloudInitConfig{
			Hostname: name,
			User:     "dev",
			SSHKeys:  sshKeys,
			Packages: pkgs,
			RunCmds: []string{
				// Install Docker engine via official script.
				"curl -fsSL https://get.docker.com | sh",
				"usermod -aG docker dev",
				// Set zsh as default shell.
				"chsh -s /bin/zsh dev",
			},
		},
		CreatedAt: time.Now(),
	}
}
