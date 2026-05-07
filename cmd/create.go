package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Benehiko/vee/images"
	"github.com/Benehiko/vee/templates"
	"github.com/Benehiko/vee/vm"
	"github.com/spf13/cobra"
)

var (
	createTemplate      string
	createMemory        string
	createCPUs          int
	createDisk          string
	createNicMode       string
	createNicBridge     string
	createSpicePort     int
	createUEFI          bool
	createGPUMode       string
	createGPUPCI        string
	createAntiDetect    bool
	createVirtiofsDir   string
	createVirtiofsTag   string
	createSSHKeyFile    string
	createUser          string
	createSSHShare      bool
	createHeadless      bool
	createSSHPort       int
	createDistro        string
	createDistroVersion string
)

var createCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new VM",
	Long: `Create a new VM and persist its configuration.

Templates apply sane defaults automatically:
  ubuntu-server  Ubuntu 24.04 Server, UEFI, user mode NIC
  gaming         GPU passthrough, 16G RAM, 6 CPUs, anti-detect, bridge NIC
  torrent        Lightweight 4G / 2 CPUs, SPICE, qbittorrent-nox via cloud-init
  devbox         8G / 4 CPUs, Docker + zsh via cloud-init (supports --distro)
  server         8G / 2 CPUs, openssh + ufw + fail2ban via cloud-init (supports --distro)
  windows        24G / 4 CPUs, UEFI secboot, TPM 2.0

Supported distros for devbox/server: ubuntu, arch, fedora
Use --distro-version latest (default) or a specific version string.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		var sshKeys []string
		if createSSHKeyFile != "" {
			data, err := os.ReadFile(createSSHKeyFile)
			if err != nil {
				return fmt.Errorf("read SSH key file: %w", err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					sshKeys = append(sshKeys, line)
				}
			}
		}

		var cfg *vm.VMConfig

		switch createTemplate {
		case "gaming":
			cfg = templates.NewGamingConfig(prov, name, createGPUPCI, createVirtiofsDir)
		case "torrent":
			cfg = templates.NewTorrentConfig(prov, name, createSpicePort)
		case "devbox":
			var err error
			cfg, err = templates.NewDevboxConfig(cmd.Context(), prov, name, sshKeys, createDistro, createDistroVersion)
			if err != nil {
				return err
			}
		case "server":
			var err error
			cfg, err = templates.NewServerConfig(cmd.Context(), prov, name, sshKeys, createDistro, createDistroVersion)
			if err != nil {
				return err
			}
		case "windows":
			cfg = templates.NewWindowsConfig(prov, name)
		case "ubuntu-server":
			version := images.UbuntuVersion(createDistroVersion)
			if createDistroVersion == "" || createDistroVersion == "latest" {
				version = images.KnownUbuntuVersions[0]
			}
			var err error
			cfg, err = templates.NewUbuntuServerConfig(cmd.Context(), prov, version, name)
			if err != nil {
				return err
			}
		default:
			cfg = defaultConfig(name)
		}

		// CLI flags override template defaults when explicitly set.
		if cmd.Flags().Changed("memory") {
			cfg.Memory = createMemory
		}
		if cmd.Flags().Changed("cpus") {
			cfg.CPUs = createCPUs
			cfg.Cores = createCPUs
		}
		if cmd.Flags().Changed("nic-mode") {
			cfg.NIC.Mode = createNicMode
		}
		if cmd.Flags().Changed("nic-bridge") {
			cfg.NIC.Bridge = createNicBridge
		}
		if cmd.Flags().Changed("uefi") {
			cfg.UEFI.Enabled = createUEFI
		}
		if cmd.Flags().Changed("gpu-mode") {
			cfg.GPU.Mode = vm.GPUMode(createGPUMode)
		}
		if cmd.Flags().Changed("gpu-pci") {
			cfg.GPU.PCIAddr = createGPUPCI
		}
		if cmd.Flags().Changed("anti-detect") {
			cfg.GPU.AntiDetect = createAntiDetect
		}
		if cmd.Flags().Changed("spice-port") && createSpicePort > 0 {
			if cfg.SPICE == nil {
				cfg.SPICE = &vm.SPICEConfig{DisableTicketing: true}
			}
			cfg.SPICE.Port = createSpicePort
		}
		if cmd.Flags().Changed("virtiofs-dir") && createVirtiofsDir != "" {
			tag := createVirtiofsTag
			if tag == "" {
				tag = "share"
			}
			cfg.VirtiofsMounts = append(cfg.VirtiofsMounts, vm.VirtiofsMount{
				SharedDir: createVirtiofsDir,
				Tag:       tag,
			})
		}
		if cmd.Flags().Changed("disk") && createDisk != "" {
			cfg.Disks = append([]vm.DiskConfig{{
				Size:      createDisk,
				Format:    "qcow2",
				Interface: "virtio",
				Media:     "disk",
				Cache:     "none",
			}}, cfg.Disks...)
		}
		if cmd.Flags().Changed("ssh-share") {
			cfg.SSHShare = createSSHShare
		}
		if cmd.Flags().Changed("headless") {
			cfg.Headless = createHeadless
		}
		if cmd.Flags().Changed("ssh-port") && createSSHPort > 0 {
			cfg.SSHPort = createSSHPort
		}

		mgr := vm.NewManager(prov)
		if err := mgr.Create(cmd.Context(), cfg); err != nil {
			return err
		}
		fmt.Printf("Created VM %q (template: %s)\n", name, cfg.Template)
		return nil
	},
}

func defaultConfig(name string) *vm.VMConfig {
	conf := prov.Config()
	return &vm.VMConfig{
		Name:     name,
		Template: createTemplate,
		Memory:   createMemory,
		CPUs:     createCPUs,
		Sockets:  1,
		Cores:    createCPUs,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:   createNicMode,
			Bridge: createNicBridge,
			Model:  "virtio-net-pci",
		},
		GPU: vm.GPUConfig{Mode: vm.GPUNone},
		UEFI: vm.UEFIConfig{
			Enabled: createUEFI,
		},
		CreatedAt: time.Now(),
	}
}

func init() {
	createCmd.Flags().StringVar(&createTemplate, "template", "ubuntu-server", "VM template: ubuntu-server, gaming, torrent, devbox, server, windows")
	createCmd.Flags().StringVar(&createMemory, "memory", "2G", "Memory size (overrides template default)")
	createCmd.Flags().IntVar(&createCPUs, "cpus", 2, "Number of vCPUs (overrides template default)")
	createCmd.Flags().StringVar(&createDisk, "disk", "", "Extra primary disk size (e.g. 50G)")
	createCmd.Flags().StringVar(&createNicMode, "nic-mode", "user", "NIC mode: bridge or user")
	createCmd.Flags().StringVar(&createNicBridge, "nic-bridge", "br0", "Bridge interface (when nic-mode=bridge)")
	createCmd.Flags().IntVar(&createSpicePort, "spice-port", 0, "SPICE port (0 = use template default)")
	createCmd.Flags().BoolVar(&createUEFI, "uefi", false, "Enable UEFI boot (OVMF)")
	createCmd.Flags().StringVar(&createGPUMode, "gpu-mode", "none", "GPU mode: none, virtio, passthrough")
	createCmd.Flags().StringVar(&createGPUPCI, "gpu-pci", "", "PCI address for GPU passthrough (e.g. 08:00.0)")
	createCmd.Flags().BoolVar(&createAntiDetect, "anti-detect", false, "Apply anti-hypervisor-detection CPU flags (gaming passthrough)")
	createCmd.Flags().StringVar(&createVirtiofsDir, "virtiofs-dir", "", "Host directory to share via virtiofsd")
	createCmd.Flags().StringVar(&createVirtiofsTag, "virtiofs-tag", "share", "Mount tag for the virtiofs share")
	createCmd.Flags().StringVar(&createSSHKeyFile, "ssh-keys", "", "Path to file containing SSH public keys (one per line)")
	createCmd.Flags().StringVar(&createUser, "user", "", "Default cloud-init username (overrides template default)")
	createCmd.Flags().BoolVar(&createSSHShare, "ssh-share", false, "Enable SSH agent sharing into VM via AF_VSOCK")
	createCmd.Flags().BoolVar(&createHeadless, "headless", false, "Run VM headless (no display window); SSH-only access")
	createCmd.Flags().IntVar(&createSSHPort, "ssh-port", 0, "Host port forwarded to VM port 22 (headless VMs only)")
	createCmd.Flags().StringVar(&createDistro, "distro", "ubuntu", "Base OS distro for devbox/server templates: ubuntu, arch, fedora")
	createCmd.Flags().StringVar(&createDistroVersion, "distro-version", "latest", "ISO version for the selected distro (e.g. 24.04, 2025.05.01, 42) or 'latest'")
}
