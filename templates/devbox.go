package templates

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Benehiko/vee/cloudinit"
	"github.com/Benehiko/vee/images"
	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/vm"
)

// NewDevboxConfig returns a VMConfig for a developer workstation VM.
// distro selects the base OS (ubuntu, arch, fedora); version selects the ISO version ("latest" for newest).
// sshKeys are injected into the default user's authorized_keys via cloud-init.
func NewDevboxConfig(ctx context.Context, p provider.Provider, name string, sshKeys []string, distro, version string) (*vm.VMConfig, error) {
	if distro == "" {
		distro = images.DistroUbuntu
	}
	if version == "" {
		version = "latest"
	}

	img, err := images.NewImage(p, distro, version)
	if err != nil {
		return nil, fmt.Errorf("devbox image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("devbox image download: %w", err)
	}

	conf := p.Config()
	vmDir := filepath.Join(conf.StoragePath, name)

	pkgs := cloudinit.PackagesFor(cloudinit.Distro(distro), cloudinit.CategoryDevbox)
	user := "dev"

	runCmds, writeFiles, err := devboxRunCmds(distro, user, name, pkgs)
	if err != nil {
		return nil, err
	}

	cfg := &vm.VMConfig{
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
		GPU:      vm.GPUConfig{Mode: vm.GPUNone},
		Headless: true,
		SSHPort:  deterministicSSHPort(name),
		// Cloud images use legacy BIOS boot; UEFI is not needed here.
		UEFI: vm.UEFIConfig{Enabled: false},
		Disks: []vm.DiskConfig{
			{
				// Cloud image copied per-VM so each VM gets its own writable disk.
				Path:        filepath.Join(vmDir, "storage", "disk-os.img"),
				Size:        conf.DefaultDiskSize,
				Format:      "qcow2",
				Interface:   "virtio",
				Media:       "disk",
				Cache:       "writeback",
				BackingFile: img.AbsolutePath(),
			},
		},
		CloudInit: &vm.CloudInitConfig{
			Hostname:    name,
			User:        user,
			DefaultUser: images.DefaultUser(distro),
			SSHKeys:     sshKeys,
			Packages:    pkgs,
			RunCmds:     runCmds,
			WriteFiles:  writeFiles,
		},
		CreatedAt: time.Now(),
	}

	return cfg, nil
}

func devboxRunCmds(distro, user, hostname string, pkgs []string) ([]string, []vm.CloudInitWriteFile, error) {
	vsockService := vsockSSHAgentService()

	switch distro {
	case images.DistroUbuntu:
		writeFiles := []vm.CloudInitWriteFile{
			{
				Path:        "/etc/systemd/system/vee-ssh-agent.service",
				Content:     vsockService,
				Permissions: "0644",
			},
		}
		runCmds := []string{
			"curl -fsSL https://get.docker.com | sh",
			"usermod -aG docker " + user,
			"chsh -s /bin/zsh " + user,
			"apt-get install -y socat",
			"systemctl enable --now vee-ssh-agent",
		}
		return runCmds, writeFiles, nil

	case images.DistroArch:
		archCfg, err := archinstallConfig(hostname, user, pkgs)
		if err != nil {
			return nil, nil, fmt.Errorf("archinstall config: %w", err)
		}
		writeFiles := []vm.CloudInitWriteFile{
			{
				Path:        "/tmp/archinstall.json",
				Content:     archCfg,
				Permissions: "0644",
			},
			{
				Path:        "/etc/systemd/system/vee-ssh-agent.service",
				Content:     vsockService,
				Permissions: "0644",
			},
		}
		runCmds := []string{
			"archinstall --config /tmp/archinstall.json --silent",
			"pacman -Syu --noconfirm",
			"pacman -S --noconfirm docker zsh socat",
			"systemctl enable --now docker",
			"usermod -aG docker " + user,
			"chsh -s /bin/zsh " + user,
			"systemctl enable --now vee-ssh-agent",
		}
		return runCmds, writeFiles, nil

	case images.DistroFedora:
		writeFiles := []vm.CloudInitWriteFile{
			{
				Path:        "/etc/systemd/system/vee-ssh-agent.service",
				Content:     vsockService,
				Permissions: "0644",
			},
		}
		runCmds := []string{
			"dnf install -y dnf-plugins-core",
			"dnf config-manager --add-repo https://download.docker.com/linux/fedora/docker-ce.repo",
			"dnf install -y docker-ce docker-ce-cli containerd.io zsh socat",
			"systemctl enable --now docker",
			"usermod -aG docker " + user,
			"chsh -s /bin/zsh " + user,
			"systemctl enable --now vee-ssh-agent",
		}
		return runCmds, writeFiles, nil

	default:
		return nil, nil, fmt.Errorf("unsupported distro for devbox: %s", distro)
	}
}

// vsockSSHAgentService returns the systemd unit content for write_files.
func vsockSSHAgentService() string {
	return `[Unit]
Description=vee SSH agent vsock bridge
After=network.target

[Service]
Type=simple
ExecStartPre=/bin/mkdir -p /run/vee
ExecStart=/usr/bin/socat UNIX-LISTEN:/run/vee/ssh_agent.sock,fork,mode=0600 VSOCK-CONNECT:2:2222
Restart=on-failure

[Install]
WantedBy=multi-user.target`
}

// archinstallConfig builds a minimal archinstall JSON configuration.
func archinstallConfig(hostname, user string, pkgs []string) (string, error) {
	cfg := map[string]any{
		"hostname":   hostname,
		"bootloader": "grub",
		"profile":    map[string]any{"main": "minimal"},
		"packages":   pkgs,
		"users": []map[string]any{
			{
				"username":  user,
				"!password": "vee",
				"sudo":      true,
			},
		},
		"audio":         nil,
		"kernels":       []string{"linux"},
		"mirror-region": map[string]any{},
		"disk_layouts": map[string]any{
			"main_disk_layout": map[string]any{
				"type": "default_layout",
			},
		},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
