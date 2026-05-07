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
				// Install socat for vsock SSH agent forwarding (vee ssh-share).
				"apt-get install -y socat",
				`mkdir -p /etc/systemd/system && cat >/etc/systemd/system/vee-ssh-agent.service <<'EOF'
[Unit]
Description=vee SSH agent vsock bridge
After=network.target

[Service]
Type=simple
ExecStartPre=/bin/mkdir -p /run/vee
ExecStart=/usr/bin/socat UNIX-LISTEN:/run/vee/ssh_agent.sock,fork,mode=0600 VSOCK-CONNECT:2:2222
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF`,
				"systemctl enable --now vee-ssh-agent",
			},
		},
		CreatedAt: time.Now(),
	}
}
