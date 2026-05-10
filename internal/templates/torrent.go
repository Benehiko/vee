package templates

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Benehiko/vee/internal/cloudinit"
	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/internal/vpn"
	"github.com/Benehiko/vee/provider"
)

// ShareMount maps a host directory to a guest mount point.
type ShareMount struct {
	HostDir   string // absolute path on the host
	GuestPath string // absolute path inside the VM (e.g. /downloads)
}

// NewTorrentConfig returns a VMConfig for a lightweight torrent VM with SPICE display.
// spicePort defaults to 5934 if 0. sshKeys are injected into the VM's authorized_keys.
// mounts is an optional list of host→guest directory mappings shared via virtiofs.
// nordConf, if non-nil, installs the nordvpn snap and connects via NordLynx on first boot.
// wgConf, if non-nil, injects a generic WireGuard config with a ufw kill-switch.
// vpnProvider records the provider name (e.g. "nordvpn", "generic") for display/monitoring.
// Only one of nordConf or wgConf should be set; nordConf takes precedence.
func NewTorrentConfig(ctx context.Context, p provider.Provider, name string, sshKeys []string, mounts []ShareMount, nordConf *vpn.NordVPNConfig, wgConf *vpn.WireGuardConfig, vpnProvider string, spicePort int) (*vm.VMConfig, error) {
	conf := p.Config()
	if spicePort == 0 {
		spicePort = 5934
	}

	img, err := images.NewImage(p, images.DistroUbuntu, "latest")
	if err != nil {
		return nil, fmt.Errorf("torrent image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("torrent image download: %w", err)
	}

	vmDir := filepath.Join(conf.StoragePath, name)

	// Pick the first mount's guest path as the default save path, or /downloads.
	savePath := "/downloads"
	if len(mounts) > 0 && mounts[0].GuestPath != "" {
		savePath = mounts[0].GuestPath
	}

	writeFiles := []vm.CloudInitWriteFile{
		{
			Path:        "/home/vee/.config/qBittorrent/qBittorrent.conf",
			Content:     qbittorrentConf(savePath),
			Permissions: "0600",
			Owner:       "vee:vee",
			Defer:       true,
		},
	}
	runCmds := []string{
		"ufw allow OpenSSH",
		"ufw allow 8080/tcp",
		"ufw --force enable",
		"mkdir -p /home/vee/.config/qBittorrent",
		"chown -R vee:vee /home/vee/.config",
		"systemctl enable --now qbittorrent-nox@vee",
	}

	packages := cloudinit.PackagesFor(cloudinit.Ubuntu, cloudinit.CategoryTorrent)

	switch {
	case nordConf != nil:
		// NordVPN snap approach: install snap, login with token, enable NordLynx kill-switch.
		connectCmd := "nordvpn connect"
		if nordConf.Country != "" {
			connectCmd = fmt.Sprintf("nordvpn connect %q", nordConf.Country)
		}
		nordCmds := []string{
			"snap install nordvpn",
			fmt.Sprintf("nordvpn login --token %s", nordConf.Token),
			"nordvpn set technology nordlynx",
			"nordvpn set killswitch on",
			"nordvpn set autoconnect on",
			connectCmd,
		}
		runCmds = append(nordCmds, runCmds...)

	case wgConf != nil:
		writeFiles = append(writeFiles, vm.CloudInitWriteFile{
			Path:        "/etc/wireguard/wg0.conf",
			Content:     vpn.RenderWireGuardConf(wgConf),
			Permissions: "0600",
		})
		// Kill-switch: default-deny outbound/forward, allow only on wg0 + loopback.
		wgCmds := []string{
			"ufw default deny outgoing",
			"ufw default deny forward",
			"ufw allow out on wg0",
			"ufw allow out on lo",
			"systemctl enable --now wg-quick@wg0",
		}
		runCmds = append(wgCmds, runCmds...)
		packages = append(packages, "wireguard", "resolvconf")
	}

	var virtiofsMounts []vm.VirtiofsMount
	for i, m := range mounts {
		tag := fmt.Sprintf("share%d", i)
		if m.GuestPath != "" {
			tag = strings.NewReplacer("/", "-", " ", "_").Replace(strings.TrimPrefix(m.GuestPath, "/"))
		}
		virtiofsMounts = append(virtiofsMounts, vm.VirtiofsMount{
			SharedDir: m.HostDir,
			Tag:       tag,
		})
		guestPath := m.GuestPath
		if guestPath == "" {
			guestPath = "/share" + fmt.Sprintf("%d", i)
		}
		runCmds = append([]string{
			fmt.Sprintf("mkdir -p %s", guestPath),
			fmt.Sprintf("mount -t virtiofs %s %s", tag, guestPath),
			fmt.Sprintf("chown vee:vee %s", guestPath),
		}, runCmds...)
	}

	return &vm.VMConfig{
		Name:     name,
		Template: "torrent",
		Memory:   "2G",
		CPUs:     1,
		Sockets:  1,
		Cores:    1,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:     "user",
			Model:    "virtio-net-pci",
			HostFwds: []string{"tcp:127.0.0.1:8080-:8080"},
		},
		SSHPort:        deterministicSSHPort(name),
		GPU:            vm.GPUConfig{Mode: vm.GPUNone},
		Headless:       false,
		UEFI:           vm.UEFIConfig{Enabled: false},
		VirtiofsMounts: virtiofsMounts,
		VPNProvider:    vpnProvider,
		SPICE: &vm.SPICEConfig{
			Port:             spicePort,
			DisableTicketing: true,
		},
		Disks: []vm.DiskConfig{
			{
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
			User:        "vee",
			DefaultUser: images.DefaultUser(images.DistroUbuntu),
			SSHKeys:     sshKeys,
			Packages:    packages,
			RunCmds:     runCmds,
			WriteFiles:  writeFiles,
		},
		CreatedAt: time.Now(),
	}, nil
}
