package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/Benehiko/vee/internal/blockdev"
	"github.com/Benehiko/vee/internal/gpu"
	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/tui"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/internal/vm/build"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	createNoStart       bool
	createNoAutoInstall bool
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
	createMedia         []string
	createRunnerURL     string
	createRunnerLabels  []string
	createPassword      string
	createBootDisk      string
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
  jellyfin        4G / 2 CPUs, Ubuntu cloud image, Jellyfin via official APT repo,
                  Avahi mDNS so http://<name> resolves on the LAN. Attach libraries
                  with repeatable --media flags (NFS/SMB/host-dir/block/USB).
  github-runner   4G / 4 CPUs, Ubuntu cloud image, self-hosted GitHub Actions runner.
                  Uses outbound HTTPS long-polling; no inbound port forwarding required.
                  Pass --runner-url (repo or org URL) and enter the registration token
                  when prompted. Defaults to labels: self-hosted,linux,kvm.

Supported distros for devbox/server: ubuntu, arch, fedora
Use --distro-version latest (default) or a specific version string.

TrueNAS data disk passthrough (serial optional, auto-derived from path if omitted):
  vee create nas --template truenas \
    --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0S3H6:EXOS22TB-A \
    --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0WD9J:EXOS22TB-B`,
	Args: cobra.RangeArgs(0, 1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// No name + no --template: empty TUI.
		if len(args) == 0 && !cmd.Flags().Changed("template") {
			return tui.Run(cmd.Context(), prov)
		}

		var name string
		if len(args) > 0 {
			name = args[0]
		}

		// Name (or any flag) but no --template: drop into the TUI form
		// pre-filled from whatever flags the user did supply.
		if !cmd.Flags().Changed("template") {
			return tui.RunCreate(cmd.Context(), prov, name, optsFromFlags(cmd, name))
		}

		// Flag-only fast path. Templates that need interactive prompts collect
		// them here (the build package itself does no I/O).
		opts := optsFromFlags(cmd, name)
		if opts.Template == "passthrough" && (opts.NVMeDev == "" || opts.OVMFVars == "") {
			return tui.RunConfigWizard(cmd.Context(), prov, !createNoStart, name)
		}
		if opts.Template == "torrent" {
			mounts, mountErr := promptShareMounts(opts.VirtiofsDir)
			if mountErr != nil {
				return fmt.Errorf("prompt share mounts: %w", mountErr)
			}
			nordConf, wgConf, vpnProvider, vpnErr := promptVPN()
			if vpnErr != nil {
				return fmt.Errorf("VPN setup: %w", vpnErr)
			}
			opts.TorrentExtras = &build.TorrentExtras{
				Mounts:      mounts,
				NordConf:    nordConf,
				WireGuard:   wgConf,
				VPNProvider: vpnProvider,
			}
		}
		if opts.Template == "jellyfin" {
			libs, parseErr := parseMediaSpecs(createMedia)
			if parseErr != nil {
				return parseErr
			}
			// Bridge mode is required: mDNS + Jellyfin discovery don't work
			// behind QEMU user-mode NAT.
			if opts.NICMode == "user" {
				return fmt.Errorf("jellyfin template requires --nic-mode=bridge (mDNS + LAN discovery cannot traverse user-mode NAT)")
			}
			secrets, secErr := collectMediaSecrets(libs)
			if secErr != nil {
				return fmt.Errorf("collect media secrets: %w", secErr)
			}
			opts.JellyfinExtras = &build.JellyfinExtras{Libraries: libs, Secrets: secrets}
		}
		if opts.Template == "github-runner" {
			if createRunnerURL == "" {
				return fmt.Errorf("--runner-url is required for the github-runner template")
			}
			fmt.Fprint(os.Stderr, "GitHub runner registration token: ")
			tokenBytes, readErr := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if readErr != nil {
				return fmt.Errorf("read runner token: %w", readErr)
			}
			labels := createRunnerLabels
			if len(labels) == 0 {
				labels = []string{"self-hosted", "linux", "kvm"}
			}
			opts.RunnerExtras = &build.RunnerExtras{
				URL:    createRunnerURL,
				Token:  strings.TrimSpace(string(tokenBytes)),
				Labels: labels,
			}
		}

		cfg, err := build.Build(cmd.Context(), prov, opts)
		if err != nil {
			return err
		}

		// Surface the IOMMU companion audio detection that build.applyOverrides
		// does silently — keep the user-visible breadcrumb the old CLI had.
		for _, addr := range cfg.GPU.ExtraVFIOAddrs {
			fmt.Printf("Auto-detected companion audio device: %s\n", addr)
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
			wasInstalling := isInstalling(mgr, name)
			if err := mgr.Start(cmd.Context(), name, false); err != nil {
				if strings.Contains(err.Error(), "already running") {
					return err
				}
				return fmt.Errorf("auto-start: %w", err)
			}
			// If the VM powered off immediately (install pass complete), skip
			// the readiness spinner — there is nothing to wait for.
			if installPassDone(mgr, name, wasInstalling) {
				fmt.Printf("Install complete. Run 'vee start %s' to boot.\n", name)
				return nil
			}
			return runStartSpinner(cmd, mgr, name)
		}
		return nil
	},
}

// optsFromFlags collects every cobra flag into a build.Opts. Cobra's "Changed"
// semantics are mirrored by leaving values at their zero/nil-pointer when the
// user did not pass a flag, so build.applyOverrides only touches fields the
// user explicitly set.
func optsFromFlags(cmd *cobra.Command, name string) build.Opts {
	opts := build.Opts{
		Name:     name,
		Template: createTemplate,
	}
	if cmd.Flags().Changed("memory") {
		opts.Memory = createMemory
	}
	if cmd.Flags().Changed("cpus") {
		opts.CPUs = createCPUs
	}
	if cmd.Flags().Changed("distro") {
		opts.Distro = createDistro
	}
	if cmd.Flags().Changed("distro-version") {
		opts.DistroVersion = createDistroVersion
	}
	if cmd.Flags().Changed("nic-mode") {
		opts.NICMode = createNicMode
	}
	if cmd.Flags().Changed("nic-bridge") {
		opts.NICBridge = createNicBridge
	}
	if cmd.Flags().Changed("nic-mac") {
		opts.NICMAC = createNICMAC
	}
	if cmd.Flags().Changed("disk") {
		opts.Disk = createDisk
	}
	if cmd.Flags().Changed("data-disk") {
		opts.DataDisks = createDataDisks
	}
	if cmd.Flags().Changed("boot-disk") {
		opts.BootDisk = createBootDisk
	}
	if cmd.Flags().Changed("spice-port") {
		p := createSpicePort
		opts.SPICEPort = &p
	}
	if cmd.Flags().Changed("uefi") {
		v := createUEFI
		opts.UEFI = &v
	}
	if cmd.Flags().Changed("headless") {
		v := createHeadless
		opts.Headless = &v
	}
	if cmd.Flags().Changed("anti-detect") {
		v := createAntiDetect
		opts.AntiDetect = &v
	}
	if cmd.Flags().Changed("ssh-share") {
		v := createSSHShare
		opts.SSHShare = &v
	}
	if cmd.Flags().Changed("gpu-mode") {
		opts.GPUMode = createGPUMode
	}
	if cmd.Flags().Changed("gpu-pci") {
		opts.GPUPCI = createGPUPCI
	}
	if cmd.Flags().Changed("gpu-vendor") {
		opts.GPUVendor = createGPUVendor
	}
	if cmd.Flags().Changed("virtiofs-dir") {
		opts.VirtiofsDir = createVirtiofsDir
	}
	if cmd.Flags().Changed("virtiofs-tag") {
		opts.VirtiofsTag = createVirtiofsTag
	}
	if cmd.Flags().Changed("ssh-keys") {
		opts.SSHKeyFile = createSSHKeyFile
	}
	if cmd.Flags().Changed("ssh-port") {
		opts.SSHPort = createSSHPort
	}
	if cmd.Flags().Changed("hostname") {
		opts.Hostname = createHostname
	}
	if cmd.Flags().Changed("user") {
		opts.User = createUser
	}
	if cmd.Flags().Changed("password") {
		opts.Password = createPassword
	}
	if cmd.Flags().Changed("nvme-dev") {
		opts.NVMeDev = createNVMeDev
	}
	if cmd.Flags().Changed("ovmf-vars") {
		opts.OVMFVars = createOVMFVars
	}
	if cmd.Flags().Changed("no-auto-install") {
		opts.NoAutoInstall = createNoAutoInstall
	}
	return opts
}

func init() {
	createCmd.Flags().BoolVar(&createNoStart, "no-start", false, "Create VM config without starting it")
	createCmd.Flags().BoolVar(&createNoAutoInstall, "no-auto-install", false, "Skip the auto-install pass; boot directly from the primary disk (use when the disk already has an OS)")
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
	createCmd.Flags().StringVar(&createUser, "user", "", "Guest login username (gaming-arch only; other templates hard-code their user)")
	createCmd.Flags().StringVar(&createPassword, "password", "", "Guest login password (chpasswd via cloud-init; gaming-arch defaults to the username)")
	createCmd.Flags().BoolVar(&createSSHShare, "ssh-share", false, "Enable SSH agent sharing into VM via AF_VSOCK")
	createCmd.Flags().BoolVar(&createHeadless, "headless", false, "Run VM headless (no display window); SSH-only access")
	createCmd.Flags().IntVar(&createSSHPort, "ssh-port", 0, "Host port forwarded to VM port 22 (headless VMs only)")
	createCmd.Flags().StringVar(&createDistro, "distro", "ubuntu", "Base OS distro for devbox/server templates: ubuntu, arch, fedora")
	createCmd.Flags().StringVar(&createDistroVersion, "distro-version", "latest", "ISO version for the selected distro (e.g. 24.04, 2025.05.01, 42) or 'latest'")
	createCmd.Flags().StringArrayVar(&createDataDisks, "data-disk", nil, "Host block device for passthrough data disk, optionally with serial: path[:serial] (repeatable)")
	createCmd.Flags().StringVar(&createBootDisk, "boot-disk", "", "Host block device to boot from (implies --data-disk; sets UEFI bootindex=1)")
	createCmd.Flags().StringVar(&createHostname, "hostname", "", "Hostname registered in /etc/hosts (or systemd-resolved) on start (default: VM name)")
	createCmd.Flags().StringVar(&createNVMeDev, "nvme-dev", "", "Host NVMe block device for raw boot passthrough (passthrough template)")
	createCmd.Flags().StringVar(&createOVMFVars, "ovmf-vars", "", "Path to existing OVMF_VARS.fd to reuse for UEFI state (passthrough template)")
	createCmd.Flags().StringVar(&createNICMAC, "nic-mac", "", "Fixed MAC address for the bridge NIC (passthrough template; empty = deterministic)")
	createCmd.Flags().StringVar(&createGPUVendor, "gpu-vendor", "amd", "Guest GPU vendor for driver selection: amd, nvidia, virtio (gaming-arch/gaming-bazzite templates)")
	createCmd.Flags().StringArrayVar(&createMedia, "media", nil, "Media source for jellyfin template (repeatable). Forms: hostdir:/host@/guest[:ro], nfs://server/export@/guest[:ro], smb://[user@]server/share@/guest[:ro], block:/dev/disk/by-id/...@/guest[:fstype], usb:VENDOR:PRODUCT@/guest[:fstype]")
	createCmd.Flags().StringVar(&createRunnerURL, "runner-url", "", "GitHub repo or org URL for runner registration (github-runner template)")
	createCmd.Flags().StringArrayVar(&createRunnerLabels, "runner-labels", nil, "Runner labels (github-runner template; default: self-hosted,linux,kvm)")

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
			"jellyfin\tJellyfin media server with NFS/SMB/USB/host-dir libraries + mDNS",
			"github-runner\tSelf-hosted GitHub Actions runner (outbound HTTPS, no port forwarding needed)",
		}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = createCmd.RegisterFlagCompletionFunc("gpu-vendor", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"amd", "nvidia", "virtio"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = createCmd.RegisterFlagCompletionFunc("gpu-pci", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		gpus := gpu.ListGPUAddresses()
		completions := make([]string, 0, len(gpus))
		for _, d := range gpus {
			name := gpu.LookupDeviceName(d.Vendor, d.Device)
			if name == "" {
				name = d.Vendor + ":" + d.Device
			}
			completions = append(completions, d.Address+"\t"+name)
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
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
	_ = createCmd.RegisterFlagCompletionFunc("data-disk", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		devs, err := blockdev.ListUnmounted()
		if err != nil || len(devs) == 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		completions := make([]string, 0, len(devs))
		for _, d := range devs {
			desc := d.DescribeShort()
			if desc == "" {
				desc = d.Name
			}
			completions = append(completions, d.ByIDPath+"\t"+desc)
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
	})
}
