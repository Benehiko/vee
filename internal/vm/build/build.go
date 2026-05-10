// Package build assembles a *vm.VMConfig from a set of options that mirror the
// `vee create` flag surface. Both the CLI flag handler and the TUI form
// converge on this package so the two entry points produce identical configs
// for identical inputs.
//
// Build does no interactive I/O. Templates that require interactive prompts
// (torrent VPN selection, passthrough device picker) accept their results via
// the Extras fields below; the caller is responsible for collecting them
// before calling Build.
package build

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/gpu"
	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/media"
	"github.com/Benehiko/vee/internal/mirror"
	"github.com/Benehiko/vee/internal/sshkeys"
	"github.com/Benehiko/vee/internal/templates"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/internal/vpn"
	"github.com/Benehiko/vee/provider"
)

// Opts is a flat representation of every value `vee create` can collect, from
// either flags or the TUI form. Bool pointers represent tri-state (unset / set
// to true / set to false) so we can replicate cobra's "Flags().Changed()"
// semantics — only override template defaults when the field was explicitly
// provided.
type Opts struct {
	// Required.
	Name     string
	Template string

	// Common defaults — empty string / 0 means "use template default".
	Memory string
	CPUs   int

	// Distro selection (devbox/server/docker/ubuntu-server/windows).
	Distro        string
	DistroVersion string

	// Networking.
	NICMode   string
	NICBridge string
	NICMAC    string

	// Disk options.
	Disk      string   // primary qcow2 size, e.g. "50G"
	DataDisks []string // host block devices for passthrough, repeatable

	// SPICE / display.
	SPICEPort *int
	Headless  *bool

	// UEFI.
	UEFI *bool

	// GPU.
	GPUMode    string
	GPUPCI     string
	GPUVendor  string // amd | nvidia | virtio (gaming templates)
	AntiDetect *bool

	// Virtiofs share.
	VirtiofsDir string
	VirtiofsTag string

	// SSH.
	SSHKeyFile string
	SSHShare   *bool
	SSHPort    int

	// Hostname.
	Hostname string

	// Passthrough template specifics.
	NVMeDev  string
	OVMFVars string

	// Interactive-only fields. Populated by the CLI/TUI surface before
	// calling Build; Build itself does no prompting.
	TorrentExtras  *TorrentExtras
	JellyfinExtras *JellyfinExtras
}

// TorrentExtras carries the data that the torrent template needs to be built,
// which is normally collected from interactive prompts.
type TorrentExtras struct {
	Mounts      []templates.ShareMount
	NordConf    *vpn.NordVPNConfig
	WireGuard   *vpn.WireGuardConfig
	VPNProvider string
}

// JellyfinExtras carries the media sources and any secrets (SMB passwords)
// collected from the CLI/TUI before invoking the jellyfin template.
type JellyfinExtras struct {
	Libraries []media.Source
	Secrets   map[string]string
}

// Build returns a fully-populated *vm.VMConfig for the given Opts. It does not
// persist the config — that is the caller's job (typically vm.Manager.Create).
func Build(ctx context.Context, prov provider.Provider, opts Opts) (*vm.VMConfig, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if opts.Template == "" {
		return nil, fmt.Errorf("template is required")
	}

	sshKeys, err := loadSSHKeys(opts.SSHKeyFile)
	if err != nil {
		return nil, err
	}

	cfg, err := configFromTemplate(ctx, prov, opts, sshKeys)
	if err != nil {
		return nil, err
	}

	applyOverrides(cfg, opts, prov)
	applyMirror(ctx, cfg, prov)
	return cfg, nil
}

// applyMirror wires the host pacman caching proxy into Arch cloud-init
// configs. It is a no-op for non-Arch templates, for VMs whose CloudInit is
// nil (the gaming installer scripts handle their own mirror config), and when
// the provider's mirror mode resolves to disabled.
func applyMirror(ctx context.Context, cfg *vm.VMConfig, prov provider.Provider) {
	if cfg.CloudInit == nil {
		return
	}
	if !isArchDistro(cfg) {
		return
	}
	pc := prov.Config()
	d := mirror.Resolve(ctx, mirror.ParseMode(pc.MirrorMode), cfg.NIC.Mode, pc.MirrorAddress)
	if !d.Enabled {
		if d.Reason != "" {
			prov.Logger().Info("mirror skipped", zap.String("vm", cfg.Name), zap.String("reason", d.Reason))
		}
		return
	}
	cfg.CloudInit.WriteFiles = append(cfg.CloudInit.WriteFiles, vm.CloudInitWriteFile{
		Path:        "/etc/pacman.d/mirrorlist",
		Content:     mirror.PacmanMirrorlistContent(d.GuestURL),
		Permissions: "0644",
	})
	prov.Logger().Info("mirror enabled", zap.String("vm", cfg.Name), zap.String("url", d.GuestURL))
}

// isArchDistro reports whether the VM cloud-init was built for Arch. Templates
// always set DefaultUser via images.DefaultUser; we match on that user as the
// stable signal (Arch's default cloud-image user is "arch").
func isArchDistro(cfg *vm.VMConfig) bool {
	if cfg.CloudInit == nil {
		return false
	}
	return cfg.CloudInit.DefaultUser == images.DefaultUser(images.DistroArch)
}

// loadSSHKeys reads the user's --ssh-keys file (if any) and always appends
// the vee-managed keypair so `vee ssh` works without manual configuration.
func loadSSHKeys(path string) ([]string, error) {
	var keys []string
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read SSH key file: %w", err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				keys = append(keys, line)
			}
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}
	veePubKey, _, err := sshkeys.EnsureVeeKeyPair(home)
	if err != nil {
		return nil, fmt.Errorf("vee keypair: %w", err)
	}
	keys = append(keys, veePubKey)
	return keys, nil
}

func configFromTemplate(ctx context.Context, prov provider.Provider, opts Opts, sshKeys []string) (*vm.VMConfig, error) {
	gamingOpts := func() templates.GamingOptions {
		return templates.GamingOptions{
			VirtiofsMountDir: opts.VirtiofsDir,
			GPUVendor:        resolveGPUVendor(opts.GPUVendor),
			Passthrough:      opts.GPUMode == "passthrough",
			PCIAddr:          opts.GPUPCI,
			Bridge:           opts.NICBridge,
			MAC:              opts.NICMAC,
		}
	}

	switch opts.Template {
	case "gaming-arch":
		return templates.NewGamingArchConfig(ctx, prov, opts.Name, sshKeys, gamingOpts())
	case "gaming-bazzite":
		return templates.NewGamingBazziteConfig(ctx, prov, opts.Name, gamingOpts())
	case "gaming":
		// Legacy alias — gaming-arch with passthrough implied if a PCI
		// address is given even when --gpu-mode wasn't set.
		gOpts := gamingOpts()
		if opts.GPUPCI != "" {
			gOpts.Passthrough = true
		}
		return templates.NewGamingArchConfig(ctx, prov, opts.Name, sshKeys, gOpts)
	case "passthrough":
		if opts.NVMeDev == "" || opts.OVMFVars == "" {
			return nil, fmt.Errorf("passthrough template requires --nvme-dev and --ovmf-vars (or use the TUI which prompts for them)")
		}
		return templates.NewPassthroughConfig(prov, opts.Name, opts.NVMeDev, opts.OVMFVars, opts.GPUPCI, opts.VirtiofsDir, opts.NICMAC), nil
	case "torrent":
		if opts.TorrentExtras == nil {
			return nil, fmt.Errorf("torrent template requires interactive prompts (mounts, VPN); collect them and pass via Opts.TorrentExtras")
		}
		spicePort := 0
		if opts.SPICEPort != nil {
			spicePort = *opts.SPICEPort
		}
		return templates.NewTorrentConfig(ctx, prov, opts.Name, sshKeys,
			opts.TorrentExtras.Mounts, opts.TorrentExtras.NordConf,
			opts.TorrentExtras.WireGuard, opts.TorrentExtras.VPNProvider, spicePort)
	case "devbox":
		return templates.NewDevboxConfig(ctx, prov, opts.Name, sshKeys, opts.Distro, opts.DistroVersion)
	case "server":
		return templates.NewServerConfig(ctx, prov, opts.Name, sshKeys, opts.Distro, opts.DistroVersion)
	case "truenas":
		spicePort := 0
		if opts.SPICEPort != nil {
			spicePort = *opts.SPICEPort
		}
		return templates.NewTruenasConfig(ctx, prov, opts.Name, opts.DistroVersion, opts.NICBridge, spicePort, opts.DataDisks)
	case "windows":
		winVersion := images.Windows11
		if opts.DistroVersion != "" && opts.DistroVersion != "latest" {
			winVersion = images.WindowsVersion(opts.DistroVersion)
		}
		return templates.NewWindowsConfig(ctx, prov, winVersion, opts.Name)
	case "docker":
		return templates.NewDockerConfig(ctx, prov, opts.Name, sshKeys, opts.DistroVersion)
	case "jellyfin":
		var libs []media.Source
		var secrets map[string]string
		if opts.JellyfinExtras != nil {
			libs = opts.JellyfinExtras.Libraries
			secrets = opts.JellyfinExtras.Secrets
		}
		return templates.NewJellyfinConfig(ctx, prov, opts.Name, sshKeys, libs, opts.NICBridge, secrets)
	case "ubuntu-server":
		version := images.UbuntuVersion(opts.DistroVersion)
		if opts.DistroVersion == "" || opts.DistroVersion == "latest" {
			version = images.KnownUbuntuVersions[0]
		}
		return templates.NewUbuntuServerConfig(ctx, prov, version, opts.Name)
	default:
		return defaultConfig(prov, opts), nil
	}
}

// applyOverrides folds explicit Opts values onto the template-produced cfg.
// Mirrors the cobra `Flags().Changed(...)` checks of the original cmd/create.go
// so the CLI and TUI produce identical configs.
func applyOverrides(cfg *vm.VMConfig, opts Opts, prov provider.Provider) {
	_ = prov

	if opts.Memory != "" {
		cfg.Memory = opts.Memory
	}
	if opts.CPUs > 0 {
		cfg.CPUs = opts.CPUs
		cfg.Cores = opts.CPUs
	}
	if opts.NICMode != "" {
		cfg.NIC.Mode = opts.NICMode
	}
	if opts.NICBridge != "" {
		cfg.NIC.Bridge = opts.NICBridge
	}
	if opts.UEFI != nil {
		cfg.UEFI.Enabled = *opts.UEFI
	}
	if opts.GPUMode != "" {
		cfg.GPU.Mode = vm.GPUMode(opts.GPUMode)
	}
	if opts.GPUPCI != "" {
		cfg.GPU.PCIAddr = opts.GPUPCI
		// Auto-detect the companion audio function and add it to
		// ExtraVFIOAddrs. GPU passthrough requires every device qemu will
		// touch during reset to be owned by the same VFIO container, so the
		// HDMI/DP audio function on a discrete GPU must be attached
		// alongside the VGA function. We look in two places: same IOMMU
		// group (typical), and same physical device (kernels with PCIe ACS
		// can place sibling functions in separate groups — without this
		// fallback qemu fails with "depends on group N which is not owned"
		// on bus-level FLR).
		if cfg.GPU.PCIAddr != "" && len(cfg.GPU.ExtraVFIOAddrs) == 0 {
			seen := map[string]bool{cfg.GPU.PCIAddr: true}
			candidates := append([]gpu.PCIDevice{}, gpu.IOMMUGroupPeers(cfg.GPU.PCIAddr)...)
			candidates = append(candidates, gpu.SiblingFunctions(cfg.GPU.PCIAddr)...)
			for _, peer := range candidates {
				if seen[peer.Address] {
					continue
				}
				seen[peer.Address] = true
				if gpu.IsAudioDevice(peer) {
					cfg.GPU.ExtraVFIOAddrs = append(cfg.GPU.ExtraVFIOAddrs, peer.Address)
				}
			}
		}
	}
	if opts.AntiDetect != nil {
		cfg.GPU.AntiDetect = *opts.AntiDetect
	}
	if opts.SPICEPort != nil && *opts.SPICEPort > 0 {
		if cfg.SPICE == nil {
			cfg.SPICE = &vm.SPICEConfig{DisableTicketing: true}
		}
		cfg.SPICE.Port = *opts.SPICEPort
	}
	if opts.VirtiofsDir != "" {
		tag := opts.VirtiofsTag
		if tag == "" {
			tag = "share"
		}
		cfg.VirtiofsMounts = append(cfg.VirtiofsMounts, vm.VirtiofsMount{
			SharedDir: opts.VirtiofsDir,
			Tag:       tag,
		})
	}
	if opts.Disk != "" {
		cfg.Disks = append([]vm.DiskConfig{{
			Size:      opts.Disk,
			Format:    "qcow2",
			Interface: "virtio",
			Media:     "disk",
			Cache:     "none",
		}}, cfg.Disks...)
	}
	if opts.SSHShare != nil {
		cfg.SSHShare = *opts.SSHShare
	}
	if opts.Headless != nil {
		cfg.Headless = *opts.Headless
	}
	if opts.SSHPort > 0 {
		cfg.SSHPort = opts.SSHPort
	}
	if opts.Hostname != "" {
		cfg.Hostname = opts.Hostname
	} else if cfg.Hostname == "" {
		cfg.Hostname = opts.Name
	}
}

// resolveGPUVendor turns the user-provided vendor string into the strongly
// typed GPUVendor used by the gaming templates. An empty string defers to
// host detection, then to AMD as the final fallback.
func resolveGPUVendor(v string) templates.GPUVendor {
	switch strings.ToLower(v) {
	case "amd":
		return templates.GPUVendorAMD
	case "nvidia":
		return templates.GPUVendorNvidia
	case "virtio":
		return templates.GPUVendorVirtio
	}
	switch gpu.DetectHostGPU() {
	case gpu.VendorAMD:
		return templates.GPUVendorAMD
	case gpu.VendorNvidia:
		return templates.GPUVendorNvidia
	case gpu.VendorVirtio:
		return templates.GPUVendorVirtio
	}
	return templates.GPUVendorAMD
}

func defaultConfig(prov provider.Provider, opts Opts) *vm.VMConfig {
	conf := prov.Config()
	memory := opts.Memory
	if memory == "" {
		memory = "2G"
	}
	cpus := opts.CPUs
	if cpus == 0 {
		cpus = 2
	}
	nicMode := opts.NICMode
	if nicMode == "" {
		nicMode = "user"
	}
	uefi := false
	if opts.UEFI != nil {
		uefi = *opts.UEFI
	}
	return &vm.VMConfig{
		Name:     opts.Name,
		Template: opts.Template,
		Memory:   memory,
		CPUs:     cpus,
		Sockets:  1,
		Cores:    cpus,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:   nicMode,
			Bridge: opts.NICBridge,
			Model:  "virtio-net-pci",
		},
		GPU:  vm.GPUConfig{Mode: vm.GPUNone},
		UEFI: vm.UEFIConfig{Enabled: uefi},
	}
}
