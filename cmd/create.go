package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/sshkeys"
	"github.com/Benehiko/vee/internal/templates"
	"github.com/Benehiko/vee/internal/tui"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	createNoStart       bool
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
	createDataDisks     []string
	createHostname      string
	createNVMeDev       string
	createOVMFVars      string
	createNICMAC        string
	createGPUVendor     string
)

var createCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new VM",
	Long: `Create a new VM and persist its configuration.

Templates apply sane defaults automatically:
  ubuntu-server   Ubuntu 24.04 Server, UEFI, user mode NIC
  gaming-arch     Arch Linux + KDE Plasma + Steam, 16G / 8 CPUs, virgl (non-passthrough)
                  or KasmVNC browser access (--gpu-mode=passthrough). Use --gpu-vendor to
                  select amd (default), nvidia, or virtio.
  gaming-bazzite  Bazzite (Fedora Atomic) gaming ISO, 16G / 8 CPUs, KDE Plasma pre-installed
  gaming          Legacy alias for gaming-arch with passthrough
  passthrough     Raw NVMe boot + GPU passthrough, 16G / 6 CPUs, SPICE, virtiofs Games
  torrent         Lightweight 4G / 2 CPUs, SPICE, qbittorrent-nox via cloud-init
  devbox          8G / 4 CPUs, Docker + zsh via cloud-init (supports --distro)
  server          8G / 2 CPUs, openssh + ufw + fail2ban via cloud-init (supports --distro)
  docker          2G / 2 CPUs, Alpine Linux, Docker daemon on tcp://localhost:2375
  windows         24G / 4 CPUs, UEFI secboot, TPM 2.0
  truenas         4G / 1 CPU, UEFI, AHCI OS disk, bridge NIC, SPICE display

Supported distros for devbox/server: ubuntu, arch, fedora
Use --distro-version latest (default) or a specific version string.

TrueNAS data disk passthrough (serial optional, auto-derived from path if omitted):
  vee create nas --template truenas \
    --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0S3H6:EXOS22TB-A \
    --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0WD9J:EXOS22TB-B`,
	Args: cobra.RangeArgs(0, 1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			p, err := provider.NewProvider()
			if err != nil {
				return err
			}
			return tui.Run(cmd.Context(), p)
		}
		name := args[0]

		var sshKeys []string
		if createSSHKeyFile != "" {
			data, err := os.ReadFile(createSSHKeyFile)
			if err != nil {
				return fmt.Errorf("read SSH key file: %w", err)
			}
			for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					sshKeys = append(sshKeys, line)
				}
			}
		}

		// Always inject the vee-managed keypair so VMs are accessible without
		// requiring the user to pass --ssh-keys on every create.
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("get home dir: %w", err)
		}
		veePubKey, _, err := sshkeys.EnsureVeeKeyPair(home)
		if err != nil {
			return fmt.Errorf("vee keypair: %w", err)
		}
		sshKeys = append(sshKeys, veePubKey)

		var cfg *vm.VMConfig

		switch createTemplate {
		case "gaming-arch":
			var err error
			cfg, err = templates.NewGamingArchConfig(cmd.Context(), prov, name, sshKeys, templates.GamingOptions{
				VirtiofsMountDir: createVirtiofsDir,
				GPUVendor:        templates.GPUVendor(createGPUVendor),
				Passthrough:      createGPUMode == "passthrough",
				PCIAddr:          createGPUPCI,
				Bridge:           createNicBridge,
				MAC:              createNICMAC,
			})
			if err != nil {
				return err
			}
		case "gaming-bazzite":
			var err error
			cfg, err = templates.NewGamingBazziteConfig(cmd.Context(), prov, name, templates.GamingOptions{
				VirtiofsMountDir: createVirtiofsDir,
				GPUVendor:        templates.GPUVendor(createGPUVendor),
				Passthrough:      createGPUMode == "passthrough",
				PCIAddr:          createGPUPCI,
				Bridge:           createNicBridge,
				MAC:              createNICMAC,
			})
			if err != nil {
				return err
			}
		case "gaming":
			// Legacy alias — delegates to gaming-arch with passthrough.
			var err error
			cfg, err = templates.NewGamingArchConfig(cmd.Context(), prov, name, sshKeys, templates.GamingOptions{
				VirtiofsMountDir: createVirtiofsDir,
				GPUVendor:        templates.GPUVendor(createGPUVendor),
				Passthrough:      createGPUMode == "passthrough" || createGPUPCI != "",
				PCIAddr:          createGPUPCI,
				Bridge:           createNicBridge,
				MAC:              createNICMAC,
			})
			if err != nil {
				return err
			}
		case "passthrough":
			if createNVMeDev == "" || createOVMFVars == "" {
				return tui.RunConfigWizard(cmd.Context(), prov, !createNoStart, name)
			}
			cfg = templates.NewPassthroughConfig(prov, name, createNVMeDev, createOVMFVars, createGPUPCI, createVirtiofsDir, createNICMAC)
		case "torrent":
			mounts, mountErr := promptShareMounts(createVirtiofsDir)
			if mountErr != nil {
				return fmt.Errorf("prompt share mounts: %w", mountErr)
			}
			nordConf, wgConf, vpnProvider, vpnErr := promptVPN()
			if vpnErr != nil {
				return fmt.Errorf("VPN setup: %w", vpnErr)
			}
			cfg, err = templates.NewTorrentConfig(cmd.Context(), prov, name, sshKeys, mounts, nordConf, wgConf, vpnProvider, createSpicePort)
			if err != nil {
				return err
			}
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
		case "truenas":
			var err error
			cfg, err = templates.NewTruenasConfig(cmd.Context(), prov, name, createDistroVersion, createNicBridge, createSpicePort, createDataDisks)
			if err != nil {
				return err
			}
		case "windows":
			winVersion := images.Windows11
			if createDistroVersion != "" && createDistroVersion != "latest" {
				winVersion = images.WindowsVersion(createDistroVersion)
			}
			var err error
			cfg, err = templates.NewWindowsConfig(cmd.Context(), prov, winVersion, name)
			if err != nil {
				return err
			}
		case "docker":
			var err error
			cfg, err = templates.NewDockerConfig(cmd.Context(), prov, name, sshKeys, createDistroVersion)
			if err != nil {
				return err
			}
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
		if cmd.Flags().Changed("hostname") {
			cfg.Hostname = createHostname
		} else if cfg.Hostname == "" {
			// Default: use VM name as hostname for any VM.
			cfg.Hostname = name
		}

		mgr := vm.NewManager(prov)
		if err := mgr.Create(cmd.Context(), cfg); err != nil {
			return err
		}
		fmt.Printf("Created VM %q (template: %s)\n", name, cfg.Template)

		if !createNoStart {
			stdinReader := bufio.NewReader(os.Stdin)
			mgr.PromptFn = func(prompt string) (string, error) {
				fmt.Fprint(os.Stderr, prompt)
				if strings.Contains(strings.ToLower(prompt), "password") {
					pw, err := term.ReadPassword(int(os.Stdin.Fd()))
					fmt.Fprintln(os.Stderr)
					return string(pw), err
				}
				line, err := stdinReader.ReadString('\n')
				return strings.TrimRight(line, "\r\n"), err
			}
			if err := mgr.Start(cmd.Context(), name, false); err != nil {
				return fmt.Errorf("auto-start: %w", err)
			}
			return runStartSpinner(cmd, mgr, name)
		}
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
	createCmd.Flags().BoolVar(&createNoStart, "no-start", false, "Create VM config without starting it")
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
	createCmd.Flags().StringArrayVar(&createDataDisks, "data-disk", nil, "Host block device for passthrough data disk, optionally with serial: path[:serial] (repeatable)")
	createCmd.Flags().StringVar(&createHostname, "hostname", "", "Hostname registered in /etc/hosts (or systemd-resolved) on start (default: VM name)")
	createCmd.Flags().StringVar(&createNVMeDev, "nvme-dev", "", "Host NVMe block device for raw boot passthrough (passthrough template)")
	createCmd.Flags().StringVar(&createOVMFVars, "ovmf-vars", "", "Path to existing OVMF_VARS.fd to reuse for UEFI state (passthrough template)")
	createCmd.Flags().StringVar(&createNICMAC, "nic-mac", "", "Fixed MAC address for the bridge NIC (passthrough template; empty = deterministic)")
	createCmd.Flags().StringVar(&createGPUVendor, "gpu-vendor", "amd", "Guest GPU vendor for driver selection: amd, nvidia, virtio (gaming-arch/gaming-bazzite templates)")

	_ = createCmd.RegisterFlagCompletionFunc("template", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{
			"ubuntu-server\tUbuntu 24.04 Server",
			"gaming-arch\tArch Linux + KDE Plasma gaming VM (virgl or passthrough)",
			"gaming-bazzite\tBazzite Fedora Atomic gaming ISO",
			"gaming\tLegacy gaming alias (GPU passthrough)",
			"passthrough\tRaw NVMe boot + GPU passthrough",
			"torrent\tqBittorrent VM with optional VPN",
			"devbox\tDev environment with Docker + zsh",
			"server\tMinimal server with openssh + ufw + fail2ban",
			"windows\tWindows VM with UEFI + TPM",
			"truenas\tTrueNAS SCALE VM",
			"docker\tAlpine Linux VM with Docker daemon on tcp://localhost:2375",
		}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = createCmd.RegisterFlagCompletionFunc("gpu-vendor", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"amd", "nvidia", "virtio"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = createCmd.RegisterFlagCompletionFunc("distro", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return images.SupportedDistros(), cobra.ShellCompDirectiveNoFileComp
	})
	_ = createCmd.RegisterFlagCompletionFunc("distro-version", func(c *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		distro, _ := c.Flags().GetString("distro")
		versions := images.DistroVersions(distro)
		if len(versions) == 0 {
			return []string{"latest"}, cobra.ShellCompDirectiveNoFileComp
		}
		return append([]string{"latest"}, versions...), cobra.ShellCompDirectiveNoFileComp
	})
	_ = createCmd.RegisterFlagCompletionFunc("gpu-mode", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"none", "virtio", "passthrough"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = createCmd.RegisterFlagCompletionFunc("nic-mode", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"user", "bridge"}, cobra.ShellCompDirectiveNoFileComp
	})
}
