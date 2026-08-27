package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/blockdev"
	"github.com/Benehiko/vee/internal/gpu"
	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/media"
	"github.com/Benehiko/vee/internal/platform"
	"github.com/Benehiko/vee/internal/runnercreds"
	"github.com/Benehiko/vee/internal/runnersetup"
	"github.com/Benehiko/vee/internal/templates"
	"github.com/Benehiko/vee/internal/tui"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/internal/vm/build"
)

var (
	createNoStart       bool
	createNoAutoInstall bool
	createReinstall     bool
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
	createNFSMounts     []string
	createSSHKeyFile    string
	createUser          string
	createSSHShare      bool
	createVsock         bool
	createHeadless      bool
	createSSHPort       int
	createDistro        string
	createDistroVersion string
	createIPSW          string
	createMacosvmDir    string
	createSkipFirstBoot bool
	createDataDisks     []string
	createHostname      string
	createNVMeDev       string
	createOVMFVars      string
	createNICMAC        string
	createGPUVendor     string
	createMedia         []string
	createDNSAdminUser  string

	createBitmagnetPGDir       string
	createBitmagnetWGConf      string
	createBitmagnetNordToken   string
	createBitmagnetNordCountry string
	createRunnerURL            string
	createRunnerLabels         []string
	createRunnerSSHKey         bool
	createPassword             string
	createBootDisk             string
	createBootDiskPath         string
	createNested               bool
	createEmulate              bool
	createBackend              string
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
  passthrough     Raw NVMe boot + GPU passthrough, 8G / 6 CPUs, SPICE, virtiofs Games
  torrent         Lightweight qbittorrent-nox via cloud-init, optional VPN kill-switch
                  (supports --distro: ubuntu default, alpine — smaller, iptables
                  kill-switch; NordVPN tokens work on both, via NordLynx)
  devbox          8G / 4 CPUs, Docker + zsh via cloud-init (supports --distro)
  server          8G / 2 CPUs, openssh + ufw + fail2ban via cloud-init (supports --distro)
  desktop         8G / 4 CPUs, GNOME + Mesa via cloud-init, accelerated virtio-gpu
                  (virgl). Graphical window with GDM autologin (supports --distro:
                  fedora default, ubuntu). Works on Apple Silicon (aarch64).
  omarchy         8G / 4 CPUs, Omarchy (Arch + Hyprland) desktop ISO, accelerated
                  virtio-gpu (virgl). Fully unattended: vee seeds Omarchy's
                  autoinstall with a user (--user/--password, default omarchy)
                  and your SSH keys, the installer runs hands-off and reboots
                  into the finished desktop with sshd enabled, so vee ssh works.
                  Also reachable as --distro omarchy on devbox and desktop.
                  x86_64 guest; on Apple Silicon pass --emulate to run it under
                  TCG emulation (needs qemu-system-x86_64, e.g. brew install
                  qemu — functional but slower than a native guest).
  docker          2G / 2 CPUs, Alpine Linux, Docker daemon on tcp://localhost:2375
  windows         8G / 4 CPUs, UEFI. x86_64: secboot + TPM 2.0, virtio disk,
                  default win10. arm64 (Apple Silicon): NVMe disk, ramfb display,
                  hardware checks bypassed, default win11
  truenas         6G / 2 CPUs, UEFI, AHCI OS disk, bridge NIC, SPICE display.
                  Passthrough data drives each get a dedicated iothread.
  jellyfin        4G / 2 CPUs, Ubuntu cloud image, Jellyfin via official APT repo,
                  Avahi mDNS so http://<name> resolves on the LAN. Attach libraries
                  with repeatable --media flags (NFS/SMB/host-dir/block/USB).
  dns-sink        512M / 1 CPU, Alpine Linux, AdGuard Home DNS sinkhole blocking ad
                  and malware domains for the whole LAN. Requires --nic-mode=bridge
                  so clients can reach the resolver. Admin UI on port 3000
                  (http://<name>.local:3000 via mDNS); the admin password is
                  prompted for and stored only as a bcrypt hash.
  bitmagnet       2G / 2 CPUs, Alpine Linux, bitmagnet BitTorrent DHT crawler and
                  indexer with PostgreSQL, behind a WireGuard kill-switch: if the
                  tunnel is down the guest cannot talk at all. The web UI (port
                  3333) is never exposed — reach it with vee tunnel. Pass
                  --pg-data-dir <host dir> to keep the crawled index on the host,
                  outliving the VM. With no VPN configured the DHT crawler is
                  disabled, so the guest cannot announce your address to the swarm.
                  NordVPN users: pass --nordvpn-token (optionally with
                  --nordvpn-country) to fetch a NordLynx config automatically —
                  NordLynx is WireGuard. Or export one and pass it with --wg-conf.
  github-runner   4G / 4 CPUs, Ubuntu cloud image, self-hosted GitHub Actions runner.
                  Uses outbound HTTPS long-polling; no inbound port forwarding required.
                  Pass --runner-url (repo or org URL) and enter the registration token
                  when prompted. Defaults to labels: self-hosted,linux,kvm.
  macos           8G / 4 CPUs, macOS guest on Apple's Virtualization.framework
                  (Apple Silicon hosts only). Restores from an IPSW (--ipsw latest
                  by default; ~14 GB download, cached) or imports a macosvm bundle
                  (--macosvm-dir). The restored guest is provisioned offline:
                  Setup Assistant is skipped, an admin account (--user, default
                  vee) is created with your vee SSH key authorized, and Remote
                  Login + Screen Sharing are enabled, so vee ssh works on first
                  boot. The generated GUI password is saved in the VM directory.
                  Pass --skip-first-boot to leave the guest at Setup Assistant.

Supported distros for devbox: ubuntu (default), arch, fedora, omarchy (unattended ISO install)
Supported distros for server: ubuntu (default), arch, fedora
Supported distros for torrent: ubuntu (default), alpine
Supported distros for desktop: fedora (default), ubuntu, omarchy (unattended ISO install)
Use --distro-version latest (default) or a specific version string.

TrueNAS data disk passthrough (serial optional, auto-derived from path if omitted):
  vee create nas --template truenas \
    --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0S3H6:EXOS22TB-A \
    --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0WD9J:EXOS22TB-B`,
	Args: cobra.RangeArgs(0, 1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// --nested only means anything for arm64 (aarch64) QEMU guests. Refuse
		// before any TUI opens: the create form hides the toggle on hosts that
		// cannot honour it, so a prefilled value would be invisible there —
		// and silently dropping an explicit flag is worse.
		if createNested && platform.DefaultGuestArch() != "aarch64" {
			return fmt.Errorf("--nested is only supported for arm64 (aarch64) guests; this host's guests are %s",
				platform.DefaultGuestArch())
		}

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
		// Exception: --boot-disk (or --data-disk) without a template means
		// "bare VM booting from this disk" — skip the TUI and go direct.
		if !cmd.Flags().Changed("template") &&
			!cmd.Flags().Changed("boot-disk") &&
			!cmd.Flags().Changed("data-disk") &&
			!cmd.Flags().Changed("ipsw") &&
			!cmd.Flags().Changed("macosvm-dir") &&
			!cmd.Flags().Changed("skip-first-boot") {
			return tui.RunCreate(cmd.Context(), prov, name, optsFromFlags(cmd, name))
		}

		// Flag-only fast path. Templates that need interactive prompts collect
		// them here (the build package itself does no I/O).
		opts := optsFromFlags(cmd, name)
		if opts.Template == "passthrough" && (opts.NVMeDev == "" || opts.OVMFVars == "") {
			return tui.RunConfigWizard(cmd.Context(), prov, !createNoStart, name)
		}
		if opts.Template == "torrent" {
			nfsMounts, nfsErr := parseNFSMounts(createNFSMounts)
			if nfsErr != nil {
				return nfsErr
			}
			// The guest mounts NFS itself, so it needs LAN reachability to the
			// server; user-mode NAT cannot route to it.
			if len(nfsMounts) > 0 && opts.NICMode == "user" {
				return fmt.Errorf("--nfs-mount requires --nic-mode=bridge (user-mode NAT cannot reach an NFS server on the LAN)")
			}
			// Only prompt for virtiofs shares when no NFS mount was given
			// (otherwise a fully flag-driven invocation would still block on
			// a prompt) and the host can actually back them.
			var mounts []templates.ShareMount
			if len(nfsMounts) == 0 && platform.SupportsVirtiofsd() {
				var mountErr error
				mounts, mountErr = promptShareMounts(opts.VirtiofsDir)
				if mountErr != nil {
					return fmt.Errorf("prompt share mounts: %w", mountErr)
				}
			}
			nordConf, wgConf, vpnProvider, vpnErr := promptVPN(cmd.Context())
			if vpnErr != nil {
				return fmt.Errorf("VPN setup: %w", vpnErr)
			}
			opts.TorrentExtras = &build.TorrentExtras{
				Mounts:      mounts,
				NFSMounts:   nfsMounts,
				NordConf:    nordConf,
				WireGuard:   wgConf,
				VPNProvider: vpnProvider,
			}
		}
		if opts.Template == "jellyfin" {
			libs, parseErr := media.ParseSpecs(createMedia)
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
		if opts.Template == "dns-sink" {
			// Bridge mode is required: other hosts on the LAN must be able to
			// reach the VM's resolver, which user-mode NAT cannot provide.
			if opts.NICMode == "user" {
				return fmt.Errorf("dns-sink template requires --nic-mode=bridge (LAN clients cannot reach a resolver behind user-mode NAT)")
			}
			hash, hashErr := promptDNSAdminPassword()
			if hashErr != nil {
				return hashErr
			}
			opts.DNSSinkExtras = &build.DNSSinkExtras{
				AdminUser:    createDNSAdminUser,
				PasswordHash: hash,
			}
		}
		if opts.Template == "bitmagnet" {
			extras, extrasErr := collectBitmagnetExtras()
			if extrasErr != nil {
				return extrasErr
			}
			opts.BitmagnetExtras = extras
		}
		if opts.Template == "github-runner" {
			if createRunnerURL == "" {
				return fmt.Errorf("--runner-url is required for the github-runner template")
			}
			// Prompt for a registration token only when no credential snapshot
			// exists; runnersetup restores snapshots so `vee create --reinstall`
			// rejoins GitHub as the same runner with no token and no duplicate
			// runner entry.
			promptToken := func() (string, error) {
				fmt.Fprint(os.Stderr, "GitHub runner registration token: ")
				tokenBytes, readErr := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
				if readErr != nil {
					return "", fmt.Errorf("read runner token: %w", readErr)
				}
				return strings.TrimSpace(string(tokenBytes)), nil
			}
			prepared, prepErr := runnersetup.Prepare(name, createRunnerURL, createRunnerLabels, createRunnerSSHKey, promptToken)
			if prepErr != nil {
				return prepErr
			}
			opts.RunnerExtras = prepared.Extras
			if prepared.RestoredFiles > 0 {
				fmt.Fprintf(os.Stderr, "Restoring %d persisted runner credential file(s) for %q — skipping token registration.\n", prepared.RestoredFiles, name)
			}
			// Show the public key + GitHub instructions when the key was newly
			// generated (per-instance keys are always new here). Re-fetch anytime
			// with `vee runner key`.
			if prepared.KeyCreated {
				label := "global runner SSH key"
				fetch := "vee runner key"
				if createRunnerSSHKey {
					label = fmt.Sprintf("per-instance SSH key for %q", name)
					fetch = "vee runner key " + name
				}
				fmt.Fprintf(os.Stderr,
					"Generated %s. Add this public key to GitHub (account SSH key, or a per-repo read-only Deploy key):\n  %s\nRe-print anytime with: %s\n",
					label, prepared.PubKey, fetch)
			}
		}

		if createReinstall {
			mgr := vm.NewManager(prov)
			if state, serr := mgr.LoadState(name); serr == nil && state.Running {
				fmt.Fprintf(os.Stderr, "Stopping %q before reinstall...\n", name)
				if serr := mgr.Stop(cmd.Context(), name); serr != nil {
					return fmt.Errorf("stop VM before reinstall: %w", serr)
				}
			}
			if err := mgr.Delete(name); err != nil {
				return fmt.Errorf("delete VM before reinstall: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Deleted %q — reinstalling from scratch.\n", name)
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
			warnUnsupportedVirtiofs(cfg)
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
			if err := runStartSpinner(cmd, mgr, name); err != nil {
				return err
			}
			// A macOS guest cannot be granted its Screen Sharing permissions
			// until it has booted once, because macOS only creates the privacy
			// database those grants live in during that boot. Complete the cycle
			// here rather than leaving the user to discover that a brand-new
			// guest refuses every Screen Sharing session.
			authorizeGuestScreenSharing(cmd, mgr, name)

			// For a freshly-registered runner, capture its credentials to the
			// host so a later `vee create --reinstall` can restore them. A
			// restore run already has the creds, so skip. Best-effort: a
			// failure here never fails the create.
			if cfg.Template == "github-runner" && opts.RunnerExtras != nil && len(opts.RunnerExtras.RestoredCreds) == 0 {
				snapshotRunnerCreds(cmd, mgr, name)
			}
			return nil
		}
		return nil
	},
}

// authorizeGuestScreenSharing finishes the two-boot cycle a macOS guest needs
// before Screen Sharing works: wait for provisioning to complete inside the
// guest, then stop and start it, which is where vee writes the grants.
//
// It is best-effort and never fails the create. The guest is already installed
// and running by this point, SSH works regardless, and any later `vee start`
// retries the grant — so a guest that was slow to provision, or unreachable,
// costs the user one restart rather than a failed create.
func authorizeGuestScreenSharing(cmd *cobra.Command, mgr *vm.Manager, name string) {
	cfg, err := mgr.LoadConfig(name)
	if err != nil || cfg.BackendName() != backend.VZ || cfg.MacOS == nil || !cfg.MacOS.ScreenSharingGrantPending {
		return
	}

	fmt.Println("Authorizing Screen Sharing in the guest (macOS only creates the privacy database this needs on the first boot, so the guest is restarted once)...")
	out, err := mgr.AuthorizeScreenSharing(cmd.Context(), name, func(step string) {
		fmt.Printf("  %s\n", step)
	})
	reportScreenSharing(name, out, err)
}

// reportScreenSharing tells the user what the create-time grant achieved. Each
// outcome needs its own words: the guest may have been left stopped, the grant
// may be deferred to a later start, or vee may have given up on this guest for
// good — and a single "it did not work" message for all three would send the
// user looking in the wrong place.
func reportScreenSharing(name string, out vm.ScreenSharingOutcome, err error) {
	switch {
	case errors.Is(err, vm.ErrGuestLeftStopped):
		// The worst outcome to get wrong: the user asked for a running VM.
		fmt.Fprintf(os.Stderr, "Warning: %v\n"+
			"The guest is installed and provisioned but is NOT running. Start it with `vee start %s`, "+
			"which also authorizes Screen Sharing.\n", err, name)
	case err != nil && !out.GuestRunning:
		fmt.Fprintf(os.Stderr, "Warning: %v\n"+
			"The guest is not running — check `vee status %s`, then `vee start %s`.\n", err, name, name)
	case err != nil:
		// The guest is up either way; only Screen Sharing is deferred.
		fmt.Fprintf(os.Stderr, "Warning: Screen Sharing is not authorized yet: %v\n"+
			"The guest is running. Its provisioning log is /var/log/vee-firstboot.log inside the guest "+
			"(reachable with `vee ssh %s` once provisioning finishes), and vee's own log is ~/.vee/logs/vee.log. "+
			"Screen Sharing is authorized by the next `vee start %s`.\n", err, name, name)
	case out.Unsupported:
		fmt.Fprintf(os.Stderr, "Warning: this guest's privacy database is not one vee recognizes, so it cannot authorize "+
			"Screen Sharing (see ~/.vee/logs/vee.log). Enable Screen Sharing inside the guest instead; "+
			"`vee ssh %s` is unaffected.\n", name)
	case !out.Granted:
		fmt.Fprintf(os.Stderr, "Warning: Screen Sharing is not authorized yet — see ~/.vee/logs/vee.log. "+
			"The next `vee start %s` retries it.\n", name)
	default:
		fmt.Println("Screen Sharing is authorized: `vee view " + name + "`")
	}
}

// snapshotRunnerCreds polls the freshly-started runner until config.sh has
// written its credentials, then persists an age-encrypted copy to the host so a
// future `vee create --reinstall <name>` can restore the same runner identity.
// It is best-effort: registration happens asynchronously via cloud-init and can
// take a few minutes (image pulls, installdependencies.sh), so it polls within
// a bounded window and only warns on failure — the VM itself is already up.
func snapshotRunnerCreds(cmd *cobra.Command, mgr *vm.Manager, name string) {
	cfg, err := mgr.LoadConfig(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cannot snapshot runner creds (load config): %v\n", err)
		return
	}
	state, err := mgr.LoadState(name)
	if err != nil || state.SSHPort == 0 {
		fmt.Fprintf(os.Stderr, "Warning: cannot snapshot runner creds (no SSH port yet)\n")
		return
	}

	user := cfg.SSHUsername()

	id, err := runnercreds.LoadOrCreateIdentity()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cannot snapshot runner creds (age identity): %v\n", err)
		return
	}

	ssh := runnercreds.NewSSHRunner(user, "127.0.0.1", state.SSHPort, veeIdentityPath())

	// Bound the wait: ~4 minutes (24 attempts × 10s) covers cloud-init
	// registration without hanging the CLI indefinitely.
	ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
	defer cancel()

	fmt.Fprintf(os.Stderr, "Waiting for runner registration to persist credentials to the host...\n")
	if err := runnercreds.SnapshotWithRetry(ctx, ssh, id, name, 24, 10*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: runner credential snapshot not captured: %v\n"+
			"  Re-run 'vee runner snapshot %s' once the runner is online to enable reinstall-without-token.\n", err, name)
		return
	}
	fmt.Fprintf(os.Stderr, "Runner credentials persisted (encrypted) — 'vee create --reinstall %s' will rejoin GitHub without a new token.\n", name)
}

// optsFromFlags collects every cobra flag into a build.Opts. Cobra's "Changed"
// semantics are mirrored by leaving values at their zero/nil-pointer when the
// user did not pass a flag, so build.applyOverrides only touches fields the
// user explicitly set.
func optsFromFlags(cmd *cobra.Command, name string) build.Opts {
	opts := build.Opts{
		Name: name,
	}
	if cmd.Flags().Changed("template") {
		opts.Template = createTemplate
	}
	if cmd.Flags().Changed("backend") {
		opts.Backend = createBackend
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
	// --nic-mode=bridge without --nic-bridge must still get the flag's default
	// bridge. The Changed() guards above exist so unset flags do not clobber
	// values the TUI prefilled, but that also means an unset --nic-bridge never
	// reaches opts — QEMU then gets "br=" and the bridge helper fails with
	// "access denied by acl file", which reads like a permissions problem
	// rather than a missing interface name.
	if opts.NICMode == "bridge" && opts.NICBridge == "" {
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
	if cmd.Flags().Changed("boot-disk-path") {
		opts.BootDiskPath = createBootDiskPath
	}
	if cmd.Flags().Changed("ipsw") || cmd.Flags().Changed("macosvm-dir") || cmd.Flags().Changed("skip-first-boot") {
		opts.MacOSExtras = &build.MacOSExtras{
			IPSW:          createIPSW,
			MacosvmDir:    createMacosvmDir,
			SkipFirstBoot: createSkipFirstBoot,
		}
		// These flags only mean anything for the macos template; imply it so
		// the values are never silently dropped on the TUI-prefill path.
		if opts.Template == "" {
			opts.Template = "macos"
		}
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
	if cmd.Flags().Changed("vsock") {
		v := createVsock
		opts.Vsock = &v
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
	if cmd.Flags().Changed("nested") {
		opts.Nested = createNested
	}
	if cmd.Flags().Changed("emulate") {
		opts.Emulate = createEmulate
	}
	return opts
}

func init() {
	createCmd.Flags().BoolVar(&createNoStart, "no-start", false, "Create VM config without starting it")
	createCmd.Flags().BoolVar(&createNoAutoInstall, "no-auto-install", false, "Skip the auto-install pass; boot directly from the primary disk (use when the disk already has an OS)")
	createCmd.Flags().BoolVar(&createReinstall, "reinstall", false, "Delete the existing VM (disk + config) and reinstall from scratch; stops the VM first if running")
	createCmd.Flags().StringVar(&createTemplate, "template", "ubuntu-server", "VM template: ubuntu-server, gaming, torrent, devbox, server, windows")
	createCmd.Flags().StringVar(&createBackend, "backend", "", "Virtualization backend: qemu (default) or vz — Apple's Virtualization.framework. On an Apple Silicon host, vz runs Linux cloud-image guests with native virtio + vsock (headless, NAT-only)")
	createCmd.Flags().StringVar(&createMemory, "memory", "2G", "Memory size (overrides template default)")
	createCmd.Flags().IntVar(&createCPUs, "cpus", 2, "Number of vCPUs (overrides template default)")
	createCmd.Flags().StringVar(&createDisk, "disk", "", "Attach an additional blank disk of this size, alongside the OS disk (e.g. 50G)")
	createCmd.Flags().StringVar(&createNicMode, "nic-mode", "user", "NIC mode: bridge or user")
	createCmd.Flags().StringVar(&createNicBridge, "nic-bridge", "br0", "Bridge interface (when nic-mode=bridge)")
	createCmd.Flags().IntVar(&createSpicePort, "spice-port", 0, "SPICE port (0 = use template default)")
	createCmd.Flags().BoolVar(&createUEFI, "uefi", false, "Enable UEFI boot (OVMF)")
	createCmd.Flags().BoolVar(&createNested, "nested", false, "Expose nested virtualization (EL2) to the guest so it can run KVM — Docker Desktop, KubeVirt, etc. (arm64 QEMU guests; under HVF needs QEMU >= 11.1 and an M3+ Mac on macOS 15+)")
	createCmd.Flags().BoolVar(&createEmulate, "emulate", false, "Run a guest whose image does not support this host's CPU architecture under TCG emulation — x86_64-only images (arch, bazzite, truenas, alpine, omarchy) on Apple Silicon. Functional but slower than a native guest; needs the matching system qemu (e.g. brew install qemu)")
	createCmd.Flags().StringVar(&createGPUMode, "gpu-mode", "none", "GPU mode: none, virtio, passthrough")
	createCmd.Flags().StringVar(&createGPUPCI, "gpu-pci", "", "PCI address for GPU passthrough (e.g. 08:00.0)")
	createCmd.Flags().BoolVar(&createAntiDetect, "anti-detect", false, "Apply anti-hypervisor-detection CPU flags (gaming passthrough)")
	createCmd.Flags().StringVar(&createVirtiofsDir, "virtiofs-dir", "", "Host directory to share via virtiofsd (Linux hosts only)")
	createCmd.Flags().StringVar(&createVirtiofsTag, "virtiofs-tag", "share", "Mount tag for the virtiofs share")
	createCmd.Flags().StringArrayVar(&createNFSMounts, "nfs-mount", nil, "NFS export mounted inside the guest as SERVER:EXPORT:GUESTPATH (repeatable; torrent template; requires --nic-mode=bridge)")
	createCmd.Flags().StringVar(&createSSHKeyFile, "ssh-keys", "", "Path to file containing SSH public keys (one per line)")
	createCmd.Flags().StringVar(&createUser, "user", "", "Guest login username (gaming-arch, omarchy and macos templates; others hard-code their user)")
	createCmd.Flags().StringVar(&createPassword, "password", "", "Guest login password (gaming-arch and omarchy default to the username)")
	createCmd.Flags().BoolVar(&createSSHShare, "ssh-share", false, "Enable SSH agent sharing into VM via AF_VSOCK")
	createCmd.Flags().BoolVar(&createVsock, "vsock", false, "Attach a virtio-vsock device for a private host<->guest channel")
	createCmd.Flags().BoolVar(&createHeadless, "headless", false, "Run VM headless (no display window); SSH-only access")
	createCmd.Flags().IntVar(&createSSHPort, "ssh-port", 0, "Host port forwarded to VM port 22 (user-mode NAT; most templates default to a stable per-name port). Change later with `vee config <name> --ssh-port`")
	createCmd.Flags().StringVar(&createDistro, "distro", "ubuntu", "Base OS distro for devbox/server/desktop/torrent templates: ubuntu, arch, fedora, alpine, omarchy (see template help for which distros each supports)")
	createCmd.Flags().StringVar(&createDistroVersion, "distro-version", "latest", "ISO version for the selected distro (e.g. 24.04, 2025.05.01, 42) or 'latest'")
	createCmd.Flags().StringVar(&createIPSW, "ipsw", "", "macos template: restore image — 'latest', an https URL, or a local .ipsw path")
	createCmd.Flags().StringVar(&createMacosvmDir, "macosvm-dir", "", "macos template: import an existing macosvm bundle directory instead of restoring")
	createCmd.Flags().BoolVar(&createSkipFirstBoot, "skip-first-boot", false, "macos template: skip offline provisioning (guest boots into Setup Assistant)")
	createCmd.Flags().StringArrayVar(&createDataDisks, "data-disk", nil, "Host block device or existing disk image file for a data disk, optionally with serial: path[:serial] (repeatable)")
	createCmd.Flags().StringVar(&createBootDisk, "boot-disk", "", "Host block device or existing disk image file (qcow2/raw/...) to boot from (implies --data-disk; sets UEFI bootindex=1)")
	createCmd.Flags().StringVar(&createBootDiskPath, "boot-disk-path", "", "Host directory for the managed boot qcow2 disk (default: <storage_path>/<name>/storage); no effect when --boot-disk names a device or image file")
	createCmd.Flags().StringVar(&createHostname, "hostname", "", "Hostname registered in /etc/hosts (or systemd-resolved) on start (default: VM name)")
	createCmd.Flags().StringVar(&createNVMeDev, "nvme-dev", "", "Host NVMe block device for raw boot passthrough (passthrough template)")
	createCmd.Flags().StringVar(&createOVMFVars, "ovmf-vars", "", "Path to existing OVMF_VARS.fd to reuse for UEFI state (passthrough template)")
	createCmd.Flags().StringVar(&createNICMAC, "nic-mac", "", "Fixed MAC address for the bridge NIC (passthrough template; empty = deterministic)")
	createCmd.Flags().StringVar(&createGPUVendor, "gpu-vendor", "amd", "Guest GPU vendor for driver selection: amd, nvidia, virtio (gaming-arch/gaming-bazzite templates)")
	createCmd.Flags().StringArrayVar(&createMedia, "media", nil, "Media source for jellyfin template (repeatable). Forms: hostdir:/host@/guest[:ro], nfs://server/export@/guest[:ro], smb://[user@]server/share@/guest[:ro], block:/dev/disk/by-id/...@/guest[:fstype], usb:VENDOR:PRODUCT@/guest[:fstype]")
	createCmd.Flags().StringVar(&createDNSAdminUser, "dns-admin-user", "admin", "AdGuard Home web UI username (dns-sink template); the password is prompted for")
	createCmd.Flags().StringVar(&createBitmagnetPGDir, "pg-data-dir", "", "host directory bind-mounted as PostgreSQL's data directory (bitmagnet template); empty keeps the database on the VM's own disk")
	createCmd.Flags().StringVar(&createBitmagnetWGConf, "wg-conf", "", "path to a WireGuard .conf backing the kill-switch (bitmagnet template); prompted for when omitted")
	createCmd.Flags().StringVar(&createBitmagnetNordToken, "nordvpn-token", "", "NordVPN access token (bitmagnet template); fetches a NordLynx WireGuard config automatically instead of --wg-conf")
	createCmd.Flags().StringVar(&createBitmagnetNordCountry, "nordvpn-country", "", "country to connect through, e.g. Netherlands (bitmagnet template, with --nordvpn-token)")
	createCmd.Flags().StringVar(&createRunnerURL, "runner-url", "", "GitHub repo or org URL for runner registration (github-runner template)")
	createCmd.Flags().StringArrayVar(&createRunnerLabels, "runner-labels", nil, "Runner labels (github-runner template; default: self-hosted,linux,kvm)")
	createCmd.Flags().BoolVar(&createRunnerSSHKey, "runner-ssh-key", false, "Generate a per-instance GitHub SSH key for this runner instead of the shared global key (github-runner template)")

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
			"omarchy\tOmarchy (Arch + Hyprland) desktop, unattended ISO install",
			"windows\tWindows VM, unattended install (UEFI; secboot+TPM on x86_64)",
			"truenas\tTrueNAS SCALE VM",
			"docker\tAlpine Linux VM with Docker daemon on tcp://localhost:2375",
			"jellyfin\tJellyfin media server with NFS/SMB/USB/host-dir libraries + mDNS",
			"dns-sink\tAlpine + AdGuard Home DNS sinkhole for ad/malware blocking (bridge NIC)",
			"bitmagnet\tAlpine + bitmagnet DHT crawler and PostgreSQL behind a WireGuard kill-switch",
			"github-runner\tSelf-hosted GitHub Actions runner (outbound HTTPS, no port forwarding needed)",
			"macos\tmacOS guest via Virtualization.framework (Apple Silicon hosts)",
		}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = createCmd.RegisterFlagCompletionFunc("ipsw", func(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		// The value is either the literal "latest" or a path to a restore
		// image. Once it starts to look like a path, narrow file completion to
		// .ipsw rather than offering every file on the host.
		if strings.ContainsAny(toComplete, "/.~") {
			return []string{"ipsw"}, cobra.ShellCompDirectiveFilterFileExt
		}
		return []string{"latest\tthe newest restore image this host can install"},
			cobra.ShellCompDirectiveNoFileComp
	})
	_ = createCmd.RegisterFlagCompletionFunc("macosvm-dir", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		// A macosvm bundle is a directory (disk image, aux storage, macosvm.json).
		return nil, cobra.ShellCompDirectiveFilterDirs
	})
	_ = createCmd.RegisterFlagCompletionFunc("gpu-vendor", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"amd", "nvidia", "virtio"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = createCmd.RegisterFlagCompletionFunc("backend", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{
			"qemu\tdetached qemu-system process (default)",
			"vz\tApple Virtualization.framework (Apple Silicon; native vsock for Linux guests)",
		}, cobra.ShellCompDirectiveNoFileComp
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
