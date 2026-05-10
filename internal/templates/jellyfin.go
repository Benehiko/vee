package templates

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Benehiko/vee/internal/cloudinit"
	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/media"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

// NewJellyfinConfig returns a VMConfig for a Jellyfin media server.
//
// Bridge networking is required: Jellyfin's HTTP UI must be reachable from the
// LAN, and mDNS hostname publishing (so http://<name> works LAN-wide) cannot
// traverse QEMU user-mode NAT. Pass an empty bridge to use the host default
// ("br0").
//
// libraries lists the media sources to attach (NFS shares, host directories,
// SMB shares, block devices, USB drives). Each is mounted at its GuestPath
// inside the VM and added to Jellyfin's accessible filesystem.
//
// secrets carries pre-resolved SMB passwords (or other prompts) collected by
// the caller before invoking the template. Keys correspond to
// media.PendingPrompt.Key returned by media.Source.Plan.
func NewJellyfinConfig(
	ctx context.Context,
	p provider.Provider,
	name string,
	sshKeys []string,
	libraries []media.Source,
	bridge string,
	secrets map[string]string,
) (*vm.VMConfig, error) {
	if bridge == "" {
		bridge = "br0"
	}

	img, err := images.NewImage(p, images.DistroUbuntu, "latest")
	if err != nil {
		return nil, fmt.Errorf("jellyfin image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("jellyfin image download: %w", err)
	}

	conf := p.Config()
	vmDir := filepath.Join(conf.StoragePath, name)

	// Gather media patches; surface any outstanding prompts as an error so the
	// caller knows to collect them before re-invoking.
	var combined media.Patch
	var missing []media.PendingPrompt
	for i, src := range libraries {
		patch, prompts, err := src.Plan(media.Ubuntu, secrets)
		if err != nil {
			return nil, fmt.Errorf("jellyfin: media[%d] (%s): %w", i, src.Kind, err)
		}
		combined.Merge(patch)
		missing = append(missing, prompts...)
	}
	if len(missing) > 0 {
		keys := make([]string, len(missing))
		for i, p := range missing {
			keys[i] = p.Key
		}
		return nil, fmt.Errorf("jellyfin: missing secrets: %v", keys)
	}

	// Base packages: jellyfin's official APT repo is added at boot via runcmd,
	// so the jellyfin package itself is installed after the repo is wired up.
	// We pre-install dependencies that survive the repo step (ufw + curl/gpg
	// needed to fetch the signing key).
	basePkgs := []string{"curl", "ca-certificates", "gnupg", "ufw", "qemu-guest-agent"}
	mdnsPkgs := cloudinit.MDNSPackages(cloudinit.Ubuntu)
	pkgs := append([]string{}, basePkgs...)
	pkgs = append(pkgs, mdnsPkgs...)
	pkgs = append(pkgs, combined.Packages...)

	writeFiles := append([]vm.CloudInitWriteFile{}, combined.WriteFiles...)

	// runcmd order matters: add the Jellyfin repo and install before media
	// mount units start (so the jellyfin user exists, though mounts are
	// independent of jellyfin and run first regardless).
	jellyfinSetup := []string{
		// Trust Jellyfin's APT signing key.
		"install -d /etc/apt/keyrings",
		"curl -fsSL https://repo.jellyfin.org/jellyfin_team.gpg.key -o /etc/apt/keyrings/jellyfin.asc",
		// Pin the repo to the running Ubuntu release.
		`. /etc/os-release && echo "Types: deb
URIs: https://repo.jellyfin.org/${ID}
Suites: ${VERSION_CODENAME}
Components: main
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/jellyfin.asc" > /etc/apt/sources.list.d/jellyfin.sources`,
		"apt-get update",
		"apt-get install -y jellyfin",
		"systemctl enable --now jellyfin",
		// vee tunnel + vee ssh on bridge mode resolve the VM IP via QGA, so
		// the guest agent has to be live before the readiness check fires.
		"systemctl enable --now qemu-guest-agent",
	}

	firewall := []string{
		"ufw allow OpenSSH",
		"ufw allow 8096/tcp", // Jellyfin HTTP
		"ufw allow 8920/tcp", // Jellyfin HTTPS
		"ufw allow 1900/udp", // DLNA discovery
		"ufw allow 7359/udp", // Jellyfin auto-discovery
	}
	firewall = append(firewall, cloudinit.MDNSFirewallCmds("ufw")...)
	firewall = append(firewall, "ufw --force enable")

	// Final runcmd order:
	//   1. Media mounts come up first so jellyfin sees the libraries on first start.
	//   2. Jellyfin repo + install.
	//   3. Firewall last (so apt fetches aren't blocked mid-install).
	//   4. mDNS enable.
	runCmds := []string{}
	runCmds = append(runCmds, combined.RunCmds...)
	runCmds = append(runCmds, jellyfinSetup...)
	runCmds = append(runCmds, firewall...)
	runCmds = append(runCmds, cloudinit.MDNSRunCmds()...)

	cfg := &vm.VMConfig{
		Name:     name,
		Template: "jellyfin",
		Memory:   "4G",
		CPUs:     2,
		Sockets:  1,
		Cores:    2,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:   "bridge",
			Bridge: bridge,
			Model:  "virtio-net-pci",
		},
		GPU:        vm.GPUConfig{Mode: vm.GPUNone},
		Headless:   true,
		GuestAgent: true,
		UEFI:       vm.UEFIConfig{Enabled: false},
		Hostname:   name,
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
		VirtiofsMounts: combined.VirtiofsMounts,
		ExtraDevices:   combined.ExtraDevices,
		CloudInit: &vm.CloudInitConfig{
			Hostname:    name,
			User:        "vee",
			DefaultUser: images.DefaultUser(images.DistroUbuntu),
			SSHKeys:     sshKeys,
			Packages:    pkgs,
			RunCmds:     runCmds,
			WriteFiles:  writeFiles,
		},
		Services: []vm.ServiceEntry{
			{Name: "jellyfin", Port: 8096, Protocol: vm.ServiceHTTP},
			{Name: "jellyfin-https", Port: 8920, Protocol: vm.ServiceHTTPS},
		},
		CreatedAt: time.Now(),
	}

	// Attach pass-through disks produced by media sources (KindBlock).
	cfg.Disks = append(cfg.Disks, combined.Disks...)

	return cfg, nil
}
