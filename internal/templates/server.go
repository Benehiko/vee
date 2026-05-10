package templates

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Benehiko/vee/internal/cloudinit"
	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

// NewServerConfig returns a VMConfig for a minimal server VM.
// distro selects the base OS (ubuntu, arch, fedora); version selects the ISO version ("latest" for newest).
// sshKeys are injected into the default user's authorized_keys.
func NewServerConfig(ctx context.Context, p provider.Provider, name string, sshKeys []string, distro, version string) (*vm.VMConfig, error) {
	if distro == "" {
		distro = images.DistroUbuntu
	}
	if version == "" {
		version = "latest"
	}

	img, err := images.NewImage(p, distro, version)
	if err != nil {
		return nil, fmt.Errorf("server image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("server image download: %w", err)
	}

	conf := p.Config()
	vmDir := filepath.Join(conf.StoragePath, name)

	pkgs := cloudinit.PackagesFor(cloudinit.Distro(distro), cloudinit.CategoryServer)
	user := "admin"

	runCmds, writeFiles, err := serverRunCmds(distro)
	if err != nil {
		return nil, err
	}

	cfg := &vm.VMConfig{
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
		GPU:      vm.GPUConfig{Mode: vm.GPUNone},
		Headless: true,
		SSHPort:  deterministicSSHPort(name),
		// Cloud images use legacy BIOS boot; UEFI is not needed here.
		UEFI: vm.UEFIConfig{Enabled: false},
		Disks: []vm.DiskConfig{
			{
				// COW overlay on the cloud image — each VM gets its own writable layer.
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

func serverRunCmds(distro string) ([]string, []vm.CloudInitWriteFile, error) {
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
			"ufw allow OpenSSH",
			"ufw --force enable",
			"systemctl enable --now fail2ban",
			"apt-get install -y socat",
			"systemctl enable --now vee-ssh-agent",
		}
		return runCmds, writeFiles, nil

	case images.DistroArch:
		writeFiles := []vm.CloudInitWriteFile{
			{
				Path:        "/etc/systemd/system/vee-ssh-agent.service",
				Content:     vsockService,
				Permissions: "0644",
			},
		}
		runCmds := []string{
			"pacman -Syu --noconfirm",
			"pacman -S --noconfirm ufw socat",
			"systemctl enable --now sshd",
			"ufw allow SSH",
			"ufw --force enable",
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
			"dnf install -y socat fail2ban",
			"systemctl enable --now sshd",
			"firewall-cmd --permanent --add-service=ssh",
			"firewall-cmd --reload",
			"systemctl enable --now fail2ban",
			"systemctl enable --now vee-ssh-agent",
		}
		return runCmds, writeFiles, nil

	default:
		return nil, nil, fmt.Errorf("unsupported distro for server: %s", distro)
	}
}
