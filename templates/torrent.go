package templates

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Benehiko/vee/cloudinit"
	"github.com/Benehiko/vee/images"
	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/vm"
	"github.com/Benehiko/vee/vpn"
)

// NewTorrentConfig returns a VMConfig for a lightweight torrent VM with SPICE display.
// spicePort defaults to 5934 if 0. sshKeys are injected into the VM's authorized_keys.
// downloadsDir is an optional host directory to share into the VM at /downloads via virtiofs.
// wgConf is an optional WireGuard config injected as /etc/wireguard/wg0.conf with a kill-switch.
// vpnProvider records the provider name (e.g. "nordvpn", "generic") for display/monitoring.
func NewTorrentConfig(ctx context.Context, p provider.Provider, name string, sshKeys []string, downloadsDir string, wgConf *vpn.WireGuardConfig, vpnProvider string, spicePort int) (*vm.VMConfig, error) {
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

	var writeFiles []vm.CloudInitWriteFile
	runCmds := []string{
		"ufw allow OpenSSH",
		"ufw allow 8080/tcp",
		"ufw --force enable",
		"systemctl enable --now qbittorrent-nox@vee",
	}

	if wgConf != nil {
		writeFiles = append(writeFiles, vm.CloudInitWriteFile{
			Path:        "/etc/wireguard/wg0.conf",
			Content:     vpn.RenderWireGuardConf(wgConf),
			Permissions: "0600",
		})
		// Kill-switch: default-deny outbound/forward, allow only on wg0 + loopback.
		// wg-quick up runs after ufw is configured so it adds its own rules on top.
		runCmds = append([]string{
			"ufw default deny outgoing",
			"ufw default deny forward",
			"ufw allow out on wg0",
			"ufw allow out on lo",
			"systemctl enable --now wg-quick@wg0",
		}, runCmds...)
	}

	var virtiofsMounts []vm.VirtiofsMount
	if downloadsDir != "" {
		virtiofsMounts = []vm.VirtiofsMount{{SharedDir: downloadsDir, Tag: "downloads"}}
		runCmds = append([]string{
			"mkdir -p /downloads",
			"mount -t virtiofs downloads /downloads",
			"chown vee:vee /downloads",
		}, runCmds...)
	}

	packages := cloudinit.PackagesFor(cloudinit.Ubuntu, cloudinit.CategoryTorrent)
	if wgConf != nil {
		packages = append(packages, "wireguard", "resolvconf")
	}

	cfg := &vm.VMConfig{
		Name:     name,
		Template: "torrent",
		Memory:   "1G",
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
	}
	return cfg, nil
}
