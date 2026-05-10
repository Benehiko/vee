package cloudinit

type Distro string

const (
	Ubuntu Distro = "ubuntu"
	Fedora Distro = "fedora"
	Arch   Distro = "arch"
)

type PackageCategory string

const (
	CategoryGaming  PackageCategory = "gaming"
	CategoryTorrent PackageCategory = "torrent"
	CategoryDevbox  PackageCategory = "devbox"
	CategoryServer  PackageCategory = "server"
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
	},
	Arch: {
		CategoryGaming: {
			"mesa",
			"vulkan-radeon",
			"xorg-server",
			"openbox",
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
