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

// NewDesktopConfig returns a VMConfig for a graphical Linux desktop VM with an
// accelerated virtio-gpu display.
//
// distro selects the base OS (fedora default, ubuntu supported); version selects
// the cloud-image version ("latest" for newest). The guest boots the distro's
// cloud image and cloud-init installs a minimal GNOME desktop plus the Mesa
// GL/Vulkan drivers, then switches to the graphical target with GDM autologin.
//
// The GPU is virtio-gpu in GL mode (vm.GPUVirtio). On an Apple Silicon host this
// resolves to virtio-gpu-gl-pci + a Cocoa display, which is hardware-accelerated
// only when the resolved QEMU was built with virglrenderer (the vee-qemu bundle,
// UTM, or a qemu-virgl tap); stock QEMU falls back to software GL.
func NewDesktopConfig(ctx context.Context, p provider.Provider, name string, sshKeys []string, distro, version string) (*vm.VMConfig, error) {
	if distro == "" {
		distro = images.DistroFedora
	}
	if version == "" {
		version = "latest"
	}
	if distro != images.DistroFedora && distro != images.DistroUbuntu {
		return nil, fmt.Errorf("unsupported distro for desktop: %s (use fedora or ubuntu)", distro)
	}

	img, err := images.NewImage(p, distro, version)
	if err != nil {
		return nil, fmt.Errorf("desktop image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("desktop image download: %w", err)
	}

	conf := p.Config()
	vmDir := filepath.Join(conf.StoragePath, name)

	pkgs := cloudinit.PackagesFor(cloudinit.Distro(distro), cloudinit.CategoryDesktop)
	defaultUser := images.DefaultUser(distro)
	runCmds, writeFiles := desktopRunCmds(distro, defaultUser)

	cfg := &vm.VMConfig{
		Name:     name,
		Template: "desktop",
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
		// Accelerated virtio-gpu (virgl). On aarch64 the manager forces UEFI on.
		GPU:      vm.GPUConfig{Mode: vm.GPUVirtio},
		Headless: false,
		SSHPort:  deterministicSSHPort(name),
		UEFI:     vm.UEFIConfig{Enabled: true},
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
			DefaultUser: defaultUser,
			SSHKeys:     sshKeys,
			Packages:    pkgs,
			RunCmds:     runCmds,
			WriteFiles:  writeFiles,
		},
		SSHUser:   defaultUser,
		CreatedAt: time.Now(),
	}

	return cfg, nil
}

// desktopRunCmds returns the cloud-init runcmd + write_files that install a
// minimal GNOME desktop and enable GDM autologin for the cloud image's default
// user. The Mesa GL/Vulkan drivers come from the CategoryDesktop package list;
// these commands add the desktop environment itself (a group install) and flip
// the system to the graphical target.
func desktopRunCmds(distro, user string) ([]string, []vm.CloudInitWriteFile) {
	vsockService := vsockSSHAgentService()
	writeFiles := []vm.CloudInitWriteFile{
		{
			Path:        "/etc/systemd/system/vee-ssh-agent.service",
			Content:     vsockService,
			Permissions: "0644",
		},
	}

	switch distro {
	case images.DistroFedora:
		writeFiles = append(writeFiles, vm.CloudInitWriteFile{
			Path:        "/etc/gdm/custom.conf",
			Content:     fmt.Sprintf("[daemon]\nAutomaticLoginEnable=True\nAutomaticLogin=%s\nWaylandEnable=True\n", user),
			Permissions: "0644",
		})
		runCmds := []string{
			"dnf install -y @base-x gnome-shell gnome-session gnome-terminal nautilus gnome-control-center gdm",
			"dnf install -y socat",
			"systemctl set-default graphical.target",
			"systemctl enable gdm",
			"systemctl enable --now vee-ssh-agent",
		}
		return runCmds, writeFiles

	case images.DistroUbuntu:
		writeFiles = append(writeFiles, vm.CloudInitWriteFile{
			Path:        "/etc/gdm3/custom.conf",
			Content:     fmt.Sprintf("[daemon]\nAutomaticLoginEnable=true\nAutomaticLogin=%s\nWaylandEnable=true\n", user),
			Permissions: "0644",
		})
		runCmds := []string{
			"apt-get update",
			"DEBIAN_FRONTEND=noninteractive apt-get install -y ubuntu-desktop-minimal gdm3 socat",
			"systemctl set-default graphical.target",
			"systemctl enable gdm3",
			"systemctl enable --now vee-ssh-agent",
		}
		return runCmds, writeFiles
	}

	return nil, writeFiles
}
