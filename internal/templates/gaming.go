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

// GPUVendor describes the guest GPU type for driver selection.
type GPUVendor string

const (
	GPUVendorAMD    GPUVendor = "amd"
	GPUVendorNvidia GPUVendor = "nvidia"
	GPUVendorVirtio GPUVendor = "virtio"
)

// GamingOptions holds optional overrides for gaming VM creation.
type GamingOptions struct {
	// VirtiofsMountDir shares a host games directory (empty = skip).
	VirtiofsMountDir string
	// GPUVendor selects guest GPU driver packages (amd, nvidia, virtio).
	// Defaults to "amd" when unset.
	GPUVendor GPUVendor
	// Passthrough enables GPU passthrough mode with KasmVNC for browser access.
	// When false, virgl / virtio-vga-gl is used (no PCI passthrough required).
	Passthrough bool
	// PCIAddr is required when Passthrough is true (e.g. "08:00.0").
	PCIAddr string
	// Bridge NIC bridge interface (default "br0").
	Bridge string
	// MAC address for the bridge NIC (empty = let QEMU pick).
	MAC string
}

// NewGamingArchConfig builds a VMConfig for an Arch Linux gaming VM.
// Arch + KDE Plasma + Wayland is used as the base; cloud-init handles setup.
func NewGamingArchConfig(ctx context.Context, p provider.Provider, name string, sshKeys []string, opts GamingOptions) (*vm.VMConfig, error) {
	if opts.GPUVendor == "" {
		opts.GPUVendor = GPUVendorAMD
	}
	if opts.Bridge == "" {
		opts.Bridge = "br0"
	}

	img, err := images.NewImage(p, images.DistroArch, "latest")
	if err != nil {
		return nil, fmt.Errorf("gaming-arch image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("gaming-arch image download: %w", err)
	}

	conf := p.Config()
	vmDir := filepath.Join(conf.StoragePath, name)

	cats := []cloudinit.PackageCategory{cloudinit.CategoryGaming}
	if !opts.Passthrough {
		cats = append(cats, cloudinit.CategoryGamingVirtigl)
	} else {
		cats = append(cats, cloudinit.CategoryGamingKasmVNC)
	}
	pkgs := cloudinit.PackagesFor(cloudinit.Arch, cats...)

	user := "gamer"
	writeFiles, runCmds := archGamingSetup(user, opts)

	gpuMode := vm.GPUVirtio
	gpuCfg := vm.GPUConfig{Mode: gpuMode}
	if opts.Passthrough {
		gpuCfg = vm.GPUConfig{
			Mode:       vm.GPUPassthrough,
			PCIAddr:    opts.PCIAddr,
			AntiDetect: true,
		}
	}

	diskPath := filepath.Join(vmDir, "storage", "disk-os.qcow2")
	cfg := &vm.VMConfig{
		Name:     name,
		Template: "gaming-arch",
		Memory:   "16G",
		CPUs:     8,
		Sockets:  1,
		Cores:    8,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:   "bridge",
			Bridge: opts.Bridge,
			Model:  "virtio-net-pci",
			MAC:    opts.MAC,
		},
		GPU:  gpuCfg,
		UEFI: vm.UEFIConfig{Enabled: true},
		Disks: []vm.DiskConfig{
			{
				Path:      diskPath,
				Size:      "80G",
				Format:    "qcow2",
				Interface: "virtio",
				Media:     "disk",
				Cache:     "writeback",
			},
			{
				Path:       img.AbsolutePath(),
				Interface:  "virtio",
				Media:      "cdrom",
				Cache:      "none",
				Readonly:   true,
				InstallISO: true,
			},
		},
		CloudInit: &vm.CloudInitConfig{
			Hostname:    name,
			User:        user,
			DefaultUser: images.DefaultUser(images.DistroArch),
			SSHKeys:     sshKeys,
			Packages:    pkgs,
			RunCmds:     runCmds,
			WriteFiles:  writeFiles,
		},
		SSHUser:   user,
		RTC:       "base=localtime,clock=host",
		CreatedAt: time.Now(),
	}

	if opts.Passthrough {
		// Add a secondary virtio-gpu for SPICE/KasmVNC alongside the VFIO GPU.
		cfg.VGA = "none"
		cfg.ExtraDevices = []string{"virtio-gpu-pci,edid=on,xres=1920,yres=1080"}
		cfg.SPICE = &vm.SPICEConfig{Port: 0, DisableTicketing: true}
		cfg.Services = []vm.ServiceEntry{
			{Name: "spice", Port: 0, Protocol: vm.ServiceSPICE}, // port filled by manager
			{Name: "kasmvnc", Port: 8443, Protocol: vm.ServiceHTTPS},
		}
	}

	if opts.VirtiofsMountDir != "" {
		cfg.VirtiofsMounts = []vm.VirtiofsMount{
			{SharedDir: opts.VirtiofsMountDir, Tag: "Games"},
		}
	}

	return cfg, nil
}

// NewGamingBazziteConfig builds a VMConfig for a Bazzite gaming VM.
// Bazzite is an immutable Fedora Atomic derivative with Steam + Proton pre-installed.
// It boots from the Bazzite ISO directly — no cloud-init (Bazzite uses its own installer).
func NewGamingBazziteConfig(ctx context.Context, p provider.Provider, name string, opts GamingOptions) (*vm.VMConfig, error) {
	if opts.GPUVendor == "" {
		opts.GPUVendor = GPUVendorAMD
	}
	if opts.Bridge == "" {
		opts.Bridge = "br0"
	}

	img, err := images.NewImage(p, images.DistroBazzite, "latest")
	if err != nil {
		return nil, fmt.Errorf("gaming-bazzite image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("gaming-bazzite image download: %w", err)
	}

	conf := p.Config()
	vmDir := filepath.Join(conf.StoragePath, name)

	gpuCfg := vm.GPUConfig{Mode: vm.GPUVirtio}
	if opts.Passthrough {
		gpuCfg = vm.GPUConfig{
			Mode:       vm.GPUPassthrough,
			PCIAddr:    opts.PCIAddr,
			AntiDetect: true,
		}
	}

	diskPath := filepath.Join(vmDir, "storage", "disk-os.qcow2")
	cfg := &vm.VMConfig{
		Name:     name,
		Template: "gaming-bazzite",
		Memory:   "16G",
		CPUs:     8,
		Sockets:  1,
		Cores:    8,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:   "bridge",
			Bridge: opts.Bridge,
			Model:  "virtio-net-pci",
			MAC:    opts.MAC,
		},
		GPU:  gpuCfg,
		UEFI: vm.UEFIConfig{Enabled: true},
		Disks: []vm.DiskConfig{
			{
				// Install target disk — Bazzite installer writes here.
				Path:      diskPath,
				Size:      "80G",
				Format:    "qcow2",
				Interface: "virtio",
				Media:     "disk",
				Cache:     "writeback",
			},
			{
				// Boot from the Bazzite ISO for first-time install.
				Path:       img.AbsolutePath(),
				Format:     "raw",
				Interface:  "ide",
				Media:      "cdrom",
				Readonly:   true,
				InstallISO: true,
			},
		},
		RTC:       "base=localtime,clock=host",
		CreatedAt: time.Now(),
	}

	if opts.Passthrough {
		cfg.VGA = "none"
		cfg.ExtraDevices = []string{"virtio-gpu-pci,edid=on,xres=1920,yres=1080"}
		cfg.SPICE = &vm.SPICEConfig{Port: 0, DisableTicketing: true}
		cfg.Services = []vm.ServiceEntry{
			{Name: "spice", Port: 0, Protocol: vm.ServiceSPICE},
			{Name: "kasmvnc", Port: 8443, Protocol: vm.ServiceHTTPS},
		}
	}

	if opts.VirtiofsMountDir != "" {
		cfg.VirtiofsMounts = []vm.VirtiofsMount{
			{SharedDir: opts.VirtiofsMountDir, Tag: "Games"},
		}
	}

	return cfg, nil
}

// archGamingSetup returns cloud-init write_files and runcmd for Arch gaming VMs.
func archGamingSetup(user string, opts GamingOptions) ([]vm.CloudInitWriteFile, []string) {
	var writeFiles []vm.CloudInitWriteFile
	var runCmds []string

	// ulimits + performance tuning
	limitsConf := `* soft nofile 524288
* hard nofile 524288
* soft memlock unlimited
* hard memlock unlimited
* soft rtprio 99
* hard rtprio 99`

	writeFiles = append(writeFiles, vm.CloudInitWriteFile{
		Path:        "/etc/security/limits.d/99-gaming.conf",
		Content:     limitsConf,
		Permissions: "0644",
	})

	// Kernel parameters: split_lock_detect off, hugepages, scheduler tuning
	sysctl := `vm.swappiness=10
kernel.split_lock_mitigate=0
vm.nr_hugepages=512
net.core.rmem_max=26214400
net.core.wmem_max=26214400`

	writeFiles = append(writeFiles, vm.CloudInitWriteFile{
		Path:        "/etc/sysctl.d/99-gaming.conf",
		Content:     sysctl,
		Permissions: "0644",
	})

	// GRUB kernel cmdline: split_lock_detect=off
	writeFiles = append(writeFiles, vm.CloudInitWriteFile{
		Path:        "/etc/default/grub.d/99-gaming.cfg",
		Content:     `GRUB_CMDLINE_LINUX_DEFAULT="$GRUB_CMDLINE_LINUX_DEFAULT split_lock_detect=off"`,
		Permissions: "0644",
	})

	// systemd-journal-upload: push guest journals to the vee host listener.
	// The host IP is resolved dynamically at boot via the default gateway.
	journalUploadConf := `[Upload]
URL=http://vee-host:19532`
	writeFiles = append(writeFiles, vm.CloudInitWriteFile{
		Path:        "/etc/systemd/journal-upload.conf",
		Content:     journalUploadConf,
		Permissions: "0644",
	})

	// sddm autologin for the gamer user
	sddmConf := fmt.Sprintf(`[Autologin]
User=%s
Session=plasma-wayland`, user)
	writeFiles = append(writeFiles, vm.CloudInitWriteFile{
		Path:        "/etc/sddm.conf.d/autologin.conf",
		Content:     sddmConf,
		Permissions: "0644",
	})

	// Base setup commands
	runCmds = append(runCmds,
		// Enable multilib for 32-bit gaming libraries
		`sed -i '/^#\[multilib\]/,/^#Include/ s/^#//' /etc/pacman.conf`,
		`pacman -Sy --noconfirm`,
		// Enable SSH
		`systemctl enable --now sshd`,
		// Enable SDDM display manager
		`systemctl enable sddm`,
		// Enable pipewire audio
		`systemctl --global enable pipewire pipewire-pulse wireplumber`,
		// Apply sysctl
		`sysctl --system`,
		// Rebuild grub config
		`mkdir -p /etc/default/grub.d`,
		`grub-mkconfig -o /boot/grub/grub.cfg`,
		// Resolve the default gateway and register it as "vee-host" in /etc/hosts,
		// then enable journal-upload so guest journals stream to the vee host listener.
		`bash -c 'GW=$(ip route show default | awk "/default/{print \$3; exit}"); if [ -n "$GW" ]; then sed -i "/vee-host/d" /etc/hosts; echo "$GW vee-host" >> /etc/hosts; fi'`,
		`systemctl enable --now systemd-journal-upload`,
		// Set gamer user password placeholder (user should change on first login)
		`echo '`+user+`:vee' | chpasswd`,
		// gamemode permissions
		`usermod -aG gamemode `+user,
	)

	if opts.Passthrough {
		// Install KasmVNC from AUR for browser-accessible display
		runCmds = append(runCmds,
			// yay for AUR
			`pacman -S --noconfirm git base-devel`,
			`sudo -u `+user+` bash -c 'cd /tmp && git clone https://aur.archlinux.org/yay.git && cd yay && makepkg -si --noconfirm'`,
			`sudo -u `+user+` yay -S --noconfirm kasmvnc`,
			// KasmVNC systemd user service
			`sudo -u `+user+` bash -c 'mkdir -p ~/.vnc && kasmvncpasswd -w vee -u `+user+`'`,
		)

		writeFiles = append(writeFiles, vm.CloudInitWriteFile{
			Path: "/etc/systemd/system/kasmvnc.service",
			Content: fmt.Sprintf(`[Unit]
Description=KasmVNC remote desktop server
After=sddm.service

[Service]
Type=simple
User=%s
ExecStart=/usr/bin/Xvnc :1 -interface 0.0.0.0 -websocketPort 8443 -cert /etc/ssl/certs/ca-certificates.crt -SecurityTypes None
Restart=on-failure

[Install]
WantedBy=multi-user.target`, user),
			Permissions: "0644",
		})
		runCmds = append(runCmds, `systemctl enable kasmvnc`)
	}

	// AMD vs Nvidia driver selection
	switch opts.GPUVendor {
	case GPUVendorNvidia:
		runCmds = append(runCmds,
			`pacman -S --noconfirm nvidia nvidia-utils lib32-nvidia-utils`,
			`systemctl enable nvidia-persistenced`,
		)
	}

	return writeFiles, runCmds
}
