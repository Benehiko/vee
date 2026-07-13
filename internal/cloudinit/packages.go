package cloudinit

type Distro string

const (
	Ubuntu  Distro = "ubuntu"
	Fedora  Distro = "fedora"
	Arch    Distro = "arch"
	Bazzite Distro = "bazzite"
)

type PackageCategory string

const (
	CategoryGaming        PackageCategory = "gaming"
	CategoryGamingVirtigl PackageCategory = "gaming-virtigl"
	CategoryGamingKasmVNC PackageCategory = "gaming-kasmvnc"
	CategoryTorrent       PackageCategory = "torrent"
	CategoryDevbox        PackageCategory = "devbox"
	CategoryServer        PackageCategory = "server"
	// CategoryDesktop holds the GL/Vulkan userspace drivers a graphical guest
	// needs for accelerated virtio-gpu (virgl). The desktop environment itself
	// is installed via the desktop template's runcmd (group install), not here.
	CategoryDesktop PackageCategory = "desktop"
)

var packageMap = map[Distro]map[PackageCategory][]string{
	Ubuntu: {
		CategoryGaming: {
			"mesa-vulkan-drivers",
			"libvulkan1",
			"vulkan-tools",
			"xorg",
			"openbox",
		},
		CategoryGamingVirtigl: {
			"mesa-vulkan-drivers",
			"libvulkan1",
			"vulkan-tools",
			"mesa-utils",
		},
		CategoryGamingKasmVNC: {},
		CategoryTorrent: {
			"qbittorrent-nox",
			"ufw",
			"curl",
			"wget",
		},
		CategoryDevbox: {
			"git",
			"curl",
			"wget",
			"build-essential",
			"zsh",
			"neovim",
			"tmux",
			"jq",
			"ca-certificates",
			"gnupg",
			"lsb-release",
		},
		CategoryServer: {
			"openssh-server",
			"ufw",
			"fail2ban",
			"htop",
			"curl",
			"wget",
			"unattended-upgrades",
		},
		CategoryDesktop: {
			"mesa-utils",
			"libgl1-mesa-dri",
			"mesa-vulkan-drivers",
			"vulkan-tools",
		},
	},
	Fedora: {
		CategoryGaming: {
			"mesa-vulkan-drivers",
			"vulkan-tools",
			"xorg-x11-server-Xorg",
			"openbox",
		},
		CategoryTorrent: {
			"qbittorrent",
			"firewalld",
			"curl",
			"wget",
		},
		CategoryDevbox: {
			"git",
			"curl",
			"wget",
			"gcc",
			"make",
			"zsh",
			"neovim",
			"tmux",
			"jq",
		},
		CategoryServer: {
			"openssh-server",
			"firewalld",
			"fail2ban",
			"htop",
			"curl",
			"wget",
		},
		CategoryDesktop: {
			"mesa-dri-drivers",
			"mesa-vulkan-drivers",
			"mesa-libGL",
			"mesa-libEGL",
			"vulkan-tools",
			"glx-utils",
		},
	},
	Arch: {
		CategoryGaming: {
			// Base gaming stack: mesa, Vulkan, KDE Plasma + Wayland, Steam, Proton, audio
			"mesa",
			"lib32-mesa",
			"vulkan-radeon",
			"lib32-vulkan-radeon",
			"vulkan-icd-loader",
			"lib32-vulkan-icd-loader",
			"vulkan-tools",
			"plasma",
			"plasma-wayland-session",
			"sddm",
			"xdg-desktop-portal-kde",
			"steam",
			"wine",
			"winetricks",
			"gamemode",
			"lib32-gamemode",
			"pipewire",
			"pipewire-pulse",
			"pipewire-alsa",
			"wireplumber",
			"openssh",
			"systemd-journal-remote",
		},
		CategoryGamingVirtigl: {
			// Extra packages for virgl / virtio-vga-gl 3D acceleration
			"mesa",
			"lib32-mesa",
			"vulkan-virtio",
			"lib32-vulkan-mesa-layers",
		},
		CategoryGamingKasmVNC: {
			// KasmVNC installed from AUR at runtime via runcmd; listed empty here.
		},
		CategoryTorrent: {
			"qbittorrent-nox",
			"ufw",
			"curl",
			"wget",
		},
		CategoryDevbox: {
			"git",
			"curl",
			"wget",
			"base-devel",
			"zsh",
			"neovim",
			"tmux",
			"jq",
		},
		CategoryServer: {
			"openssh",
			"ufw",
			"htop",
			"curl",
			"wget",
		},
	},
	// Bazzite is an immutable Fedora Atomic derivative — gaming stack is pre-installed.
	// Package installs via rpm-ostree are layered; we keep additions minimal.
	Bazzite: {
		CategoryGaming:        {},
		CategoryGamingVirtigl: {},
		CategoryGamingKasmVNC: {},
		CategoryServer: {
			"htop",
			"curl",
			"wget",
		},
	},
}

// PackagesFor returns the distro-appropriate package list for the given categories.
func PackagesFor(distro Distro, cats ...PackageCategory) []string {
	dm, ok := packageMap[distro]
	if !ok {
		return nil
	}
	seen := make(map[string]struct{})
	var pkgs []string
	for _, cat := range cats {
		for _, p := range dm[cat] {
			if _, dup := seen[p]; dup {
				continue
			}
			seen[p] = struct{}{}
			pkgs = append(pkgs, p)
		}
	}
	return pkgs
}
