package templates

import (
	"time"

	"github.com/Benehiko/vee/cloudinit"
	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/vm"
)

// NewServerConfig returns a VMConfig for a minimal Ubuntu server VM.
// sshKeys are injected into the default user's authorized_keys.
func NewServerConfig(p provider.Provider, name string, sshKeys []string) *vm.VMConfig {
	conf := p.Config()

	return &vm.VMConfig{
		Name:     name,
		Template: "server",
		Memory:   "8G",
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
		CloudInit: &vm.CloudInitConfig{
			Hostname: name,
			User:     "admin",
			SSHKeys:  sshKeys,
			Packages: cloudinit.PackagesFor(cloudinit.Ubuntu, cloudinit.CategoryServer),
			RunCmds: []string{
				"ufw allow OpenSSH",
				"ufw --force enable",
				"systemctl enable --now fail2ban",
			},
		},
		CreatedAt: time.Now(),
	}
}
