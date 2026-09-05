// Package mcpserver exposes vee's VM lifecycle over the Model Context
// Protocol so coding agents (Claude Code, Cursor, Codex, ...) can create,
// boot, inspect, and command VMs programmatically instead of shelling out to
// the CLI and parsing tab-aligned text.
//
// The server speaks MCP over stdio: stdout carries the JSON-RPC stream, so
// nothing reachable from a tool handler may write to stdout. Logging goes to
// the provider's zap logger (file, or stderr with --verbose), which is safe.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/journal"
	"github.com/Benehiko/vee/internal/media"
	"github.com/Benehiko/vee/internal/platform"
	"github.com/Benehiko/vee/internal/qemu"
	"github.com/Benehiko/vee/internal/qemubin"
	"github.com/Benehiko/vee/internal/runnersetup"
	"github.com/Benehiko/vee/internal/templates"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/internal/vm/build"
	"github.com/Benehiko/vee/internal/vpn"
	"github.com/Benehiko/vee/internal/vzhelper"
	"github.com/Benehiko/vee/provider"
)

// Run serves MCP over stdio until ctx is cancelled or the client disconnects.
func Run(ctx context.Context, prov provider.Provider, version string) error {
	s := &server{prov: prov, mgr: vm.NewManager(prov)}
	srv := mcp.NewServer(&mcp.Implementation{Name: "vee", Version: version}, nil)
	s.register(srv)
	return srv.Run(ctx, &mcp.StdioTransport{})
}

// ToolNames returns the names of every tool the MCP server registers.
// The CLI-coverage test uses it to verify that CLICoverage only references
// tools that actually exist.
func ToolNames() []string {
	s := &server{} // handlers never run during registration, so nil deps are fine
	srv := mcp.NewServer(&mcp.Implementation{Name: "vee", Version: "coverage-test"}, nil)
	return s.register(srv)
}

type server struct {
	prov provider.Provider
	mgr  *vm.Manager
}

// addTool registers a tool and records its name so register can report the
// full tool set for the drift test.
func addTool[In, Out any](srv *mcp.Server, names *[]string, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	*names = append(*names, t.Name)
	mcp.AddTool(srv, t, h)
}

func (s *server) register(srv *mcp.Server) []string {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	destructive := func(v bool) *mcp.ToolAnnotations {
		return &mcp.ToolAnnotations{DestructiveHint: &v, IdempotentHint: true}
	}
	var names []string

	addTool(srv, &names, &mcp.Tool{
		Name:        "vm_list",
		Description: "List all vee VMs with their template, resources, and run state.",
		Annotations: readOnly,
	}, s.vmList)

	addTool(srv, &names, &mcp.Tool{
		Name:        "vm_status",
		Description: "Detailed status of one VM: run state, boot phase, install state, ports, uptime, and last health-check results.",
		Annotations: readOnly,
	}, s.vmStatus)

	addTool(srv, &names, &mcp.Tool{
		Name:        "template_list",
		Description: "List the VM templates vee can create, with defaults and any extra vm_create parameters each one needs.",
		Annotations: readOnly,
	}, s.templateList)

	addTool(srv, &names, &mcp.Tool{
		Name: "vm_create",
		Description: "Create a new VM from a template (see template_list for per-template parameters). The first create of a " +
			"template may download a base image, which can take minutes. By default the VM is created stopped; boot it with " +
			"vm_start, or pass start=true. Pass reinstall=true to wipe and recreate an existing VM.",
	}, s.vmCreate)

	addTool(srv, &names, &mcp.Tool{
		Name: "vm_start",
		Description: "Boot a VM detached. By default waits until the guest is ready (SSH reachable / boot complete). " +
			"A first boot of a cloud-init template runs an install pass and may power off when done — start again to boot normally.",
	}, s.vmStart)

	addTool(srv, &names, &mcp.Tool{
		Name:        "vm_stop",
		Description: "Gracefully shut a VM down (guest powerdown). Set force=true to kill the VM process instead.",
		Annotations: destructive(false),
	}, s.vmStop)

	addTool(srv, &names, &mcp.Tool{
		Name:        "vm_delete",
		Description: "Delete one or more stopped VMs and their disks permanently (name for one, names for several). Refuses to delete a running VM — stop it first with vm_stop.",
		Annotations: destructive(true),
	}, s.vmDelete)

	addTool(srv, &names, &mcp.Tool{
		Name:        "vm_ip",
		Description: "Resolve a running VM's primary IP address (via host lease/ARP tables or the QEMU guest agent).",
		Annotations: readOnly,
	}, s.vmIP)

	addTool(srv, &names, &mcp.Tool{
		Name: "vm_exec",
		Description: "Run a shell command inside a running guest via the QEMU guest agent (/bin/sh -c). " +
			"Returns stdout, stderr, and the exit code. Requires a guest-agent-enabled template; macOS guests are not supported (use ssh).",
	}, s.vmExec)

	addTool(srv, &names, &mcp.Tool{
		Name: "vm_logs",
		Description: "Return the tail of a VM's process log (QEMU's qemu.log, or vz-helper.log for macOS guests). " +
			"Set journal=true for the guest's forwarded systemd journal, kernel=true for kernel messages only.",
		Annotations: readOnly,
	}, s.vmLogs)

	names = append(names, s.registerOps(srv)...)
	return names
}

// ---- vm_list ----

type vmSummary struct {
	Name         string `json:"name"`
	Template     string `json:"template"`
	Backend      string `json:"backend"`
	Memory       string `json:"memory"`
	CPUs         int    `json:"cpus"`
	Running      bool   `json:"running"`
	InstallState string `json:"install_state,omitempty" jsonschema:"pending while the first-boot install pass runs, ready afterwards"`
	PID          int    `json:"pid,omitempty"`
	SSHPort      int    `json:"ssh_port,omitempty" jsonschema:"host port forwarded to guest SSH (user-mode NIC)"`
	SPICEPort    int    `json:"spice_port,omitempty"`
}

type vmListOut struct {
	VMs []vmSummary `json:"vms"`
}

func (s *server) vmList(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, vmListOut, error) {
	entries, err := s.mgr.List()
	if err != nil {
		return nil, vmListOut{}, err
	}
	out := vmListOut{VMs: make([]vmSummary, 0, len(entries))}
	for _, e := range entries {
		out.VMs = append(out.VMs, summarize(e.Config, e.State))
	}
	return nil, out, nil
}

func summarize(cfg *vm.VMConfig, state *vm.VMState) vmSummary {
	return vmSummary{
		Name:         cfg.Name,
		Template:     cfg.Template,
		Backend:      string(cfg.BackendName()),
		Memory:       cfg.Memory,
		CPUs:         cfg.CPUs,
		Running:      state.Running,
		InstallState: state.InstallState,
		PID:          state.PID,
		SSHPort:      state.SSHPort,
		SPICEPort:    state.SPICEPort,
	}
}

// ---- vm_status ----

type vmNameIn struct {
	Name string `json:"name" jsonschema:"name of the VM"`
}

type vmStatusOut struct {
	vmSummary
	Hostname      string           `json:"hostname,omitempty"`
	UptimeSeconds int64            `json:"uptime_seconds,omitempty"`
	BootPhase     string           `json:"boot_phase,omitempty"`
	LastPanicLine string           `json:"last_panic_line,omitempty"`
	DesiredState  string           `json:"desired_state,omitempty"`
	HealthChecks  []vm.HealthCheck `json:"health_checks,omitempty" jsonschema:"last persisted vee check results; empty if never run"`
}

func (s *server) vmStatus(_ context.Context, _ *mcp.CallToolRequest, in vmNameIn) (*mcp.CallToolResult, vmStatusOut, error) {
	cfg, state, err := s.loadVM(in.Name)
	if err != nil {
		return nil, vmStatusOut{}, err
	}
	out := vmStatusOut{
		vmSummary:     summarize(cfg, state),
		Hostname:      cfg.Hostname,
		BootPhase:     state.BootPhase,
		LastPanicLine: state.LastPanicLine,
		DesiredState:  state.DesiredState,
		HealthChecks:  state.PostInstallChecks,
	}
	if state.Running && state.StartedAt != nil && !state.StartedAt.IsZero() {
		out.UptimeSeconds = int64(time.Since(*state.StartedAt).Seconds())
	}
	return nil, out, nil
}

// ---- template_list ----

type templateInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Params      string `json:"extra_parameters,omitempty" jsonschema:"vm_create parameters this template needs or honours beyond the common set"`
}

type templateListOut struct {
	Templates []templateInfo `json:"templates"`
}

// templateCatalog describes every template in build.KnownTemplates for
// agents. TestTemplateCatalogMatchesBuild fails if the two sets drift.
var templateCatalog = []templateInfo{
	{Name: "ubuntu-server", Description: "Ubuntu 24.04 LTS server, UEFI, user-mode NIC (default template)"},
	{Name: "devbox", Description: "Docker + zsh via cloud-init", Params: "distro (ubuntu|arch|fedora|omarchy), distro_version"},
	{Name: "server", Description: "openssh + ufw + fail2ban via cloud-init", Params: "distro (ubuntu|arch|fedora), distro_version"},
	{Name: "desktop", Description: "GNOME + Mesa, accelerated virtio-gpu (virgl)", Params: "distro (fedora|ubuntu|omarchy)"},
	{Name: "omarchy", Description: "Omarchy (Arch + Hyprland) desktop, unattended ISO install seeded with user + SSH keys (sshd enabled), accelerated virtio-gpu (virgl); x86_64-only — non-x86_64 hosts need emulate=true (TCG, slow)", Params: "distro_version, user, password, emulate"},
	{Name: "docker", Description: "Alpine Linux with the Docker daemon on tcp://localhost:2375"},
	{Name: "gaming-arch", Description: "Arch Linux + KDE Plasma + Steam, 16G / 8 CPUs", Params: "gpu_mode, gpu_pci, gpu_vendor (amd|nvidia|virtio)"},
	{Name: "gaming-bazzite", Description: "Bazzite (Fedora Atomic) gaming ISO, 16G / 8 CPUs", Params: "gpu_mode, gpu_pci, gpu_vendor"},
	{Name: "gaming", Description: "Legacy alias for gaming-arch with GPU passthrough implied when gpu_pci is set", Params: "gpu_pci"},
	{Name: "passthrough", Description: "Raw NVMe boot + GPU passthrough", Params: "requires nvme_dev and ovmf_vars"},
	{Name: "windows", Description: "Windows guest, UEFI; secure boot + TPM on x86_64, arm64 supported on Apple Silicon", Params: "distro_version (e.g. win10, win11)"},
	{Name: "truenas", Description: "TrueNAS SCALE, AHCI OS disk, bridge NIC", Params: "data_disks (host block devices, path[:serial])"},
	{Name: "macos", Description: "macOS guest on Virtualization.framework (Apple Silicon hosts only; ~14 GB IPSW download on first create)", Params: "ipsw (latest|url|path), macosvm_dir, skip_first_boot"},
	{Name: "torrent", Description: "qbittorrent-nox with optional VPN kill-switch", Params: "share_mounts, and nordvpn_token/nordvpn_country or wireguard_conf for the kill-switch"},
	{Name: "jellyfin", Description: "Jellyfin media server with NFS/SMB/host-dir media mounts; requires nic_mode=bridge", Params: "media (spec strings), media_secrets"},
	{Name: "dns-sink", Description: "Alpine + AdGuard Home DNS sinkhole blocking ad and malware domains LAN-wide; requires nic_mode=bridge", Params: "dns_admin_user, dns_admin_password_hash (bcrypt)"},
	{Name: "bitmagnet", Description: "Alpine + bitmagnet BitTorrent DHT crawler and PostgreSQL behind a WireGuard kill-switch; the web UI is never exposed, reach it with vee tunnel; without wireguard_conf the DHT crawler is disabled so the guest cannot announce its address to the swarm", Params: "wireguard_conf, or nordvpn_token/nordvpn_country to fetch a NordLynx config automatically; pg_data_dir (host directory holding the crawled index)"},
	{Name: "github-runner", Description: "Self-hosted GitHub Actions runner", Params: "requires runner_url; runner_token unless a credential snapshot exists; runner_labels, runner_ssh_key"},
}

func (s *server) templateList(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, templateListOut, error) {
	return nil, templateListOut{Templates: templateCatalog}, nil
}

// ---- vm_create ----

type shareMountIn struct {
	HostDir   string `json:"host_dir" jsonschema:"absolute path on the host"`
	GuestPath string `json:"guest_path" jsonschema:"absolute mount point inside the VM"`
}

type nfsMountIn struct {
	Server    string `json:"server" jsonschema:"NFS server host or IP, e.g. 192.168.178.76"`
	Export    string `json:"export" jsonschema:"absolute export path on the server, e.g. /mnt/Data/Movies"`
	GuestPath string `json:"guest_path" jsonschema:"absolute mount point inside the VM"`
	Options   string `json:"options,omitempty" jsonschema:"mount options; defaults to rw,hard,proto=tcp,timeo=600,retrans=2,_netdev"`
}

type vmCreateIn struct {
	Name     string `json:"name" jsonschema:"name for the new VM"`
	Template string `json:"template" jsonschema:"template to build from; see template_list"`

	// Common knobs — empty/zero means "use the template default".
	Memory        string `json:"memory,omitempty" jsonschema:"guest memory such as 4G or 8192M"`
	CPUs          int    `json:"cpus,omitempty" jsonschema:"vCPU count"`
	Disk          string `json:"disk,omitempty" jsonschema:"attach an additional blank disk of this size alongside the OS disk, such as 50G; on the macos template it sets the guest's own disk size instead"`
	Distro        string `json:"distro,omitempty" jsonschema:"distro for templates that support it: ubuntu, arch, fedora"`
	DistroVersion string `json:"distro_version,omitempty" jsonschema:"distro version, or latest"`
	Hostname      string `json:"hostname,omitempty"`
	User          string `json:"user,omitempty" jsonschema:"guest login username (honoured by gaming-arch, omarchy and macos)"`
	Password      string `json:"password,omitempty" jsonschema:"guest login password; empty means SSH key-only"`
	Nested        bool   `json:"nested,omitempty" jsonschema:"expose hardware virtualization to the guest (arm64 QEMU guests only)"`
	Emulate       bool   `json:"emulate,omitempty" jsonschema:"run a guest whose image does not support the host CPU architecture under TCG emulation (x86_64-only images on Apple Silicon); slow but functional"`
	NoAutoInstall bool   `json:"no_auto_install,omitempty" jsonschema:"skip the install pass; boot the primary disk as already installed"`
	Reinstall     bool   `json:"reinstall,omitempty" jsonschema:"stop, delete, and recreate the VM if it already exists"`
	Start         bool   `json:"start,omitempty" jsonschema:"boot the VM right after creating it (equivalent to vm_start with defaults)"`

	// Networking.
	NICMode   string `json:"nic_mode,omitempty" jsonschema:"user (default) or bridge"`
	NICBridge string `json:"nic_bridge,omitempty" jsonschema:"bridge interface for nic_mode=bridge"`
	NICMAC    string `json:"nic_mac,omitempty" jsonschema:"fixed MAC address"`
	SSHPort   int    `json:"ssh_port,omitempty" jsonschema:"host port forwarded to guest SSH (user-mode NIC)"`

	// Disks.
	DataDisks    []string `json:"data_disks,omitempty" jsonschema:"host block devices to pass through, path[:serial]"`
	BootDisk     string   `json:"boot_disk,omitempty" jsonschema:"data-disk path to mark as UEFI boot priority 1"`
	BootDiskPath string   `json:"boot_disk_path,omitempty" jsonschema:"host directory to place the managed boot disk in (e.g. a fast NVMe)"`

	// Display / firmware.
	SPICEPort *int  `json:"spice_port,omitempty"`
	Headless  *bool `json:"headless,omitempty"`
	UEFI      *bool `json:"uefi,omitempty"`

	// GPU.
	GPUMode    string `json:"gpu_mode,omitempty" jsonschema:"none, virtio, or passthrough"`
	GPUPCI     string `json:"gpu_pci,omitempty" jsonschema:"PCI address of the GPU to pass through"`
	GPUVendor  string `json:"gpu_vendor,omitempty" jsonschema:"amd, nvidia, or virtio (gaming templates)"`
	AntiDetect *bool  `json:"anti_detect,omitempty"`
	GLBackend  string `json:"gpu_gl_backend,omitempty" jsonschema:"host OpenGL backend for gpu_mode=virtio: on (Linux EGL), es (ANGLE/Metal, macOS) or core (native macOS, unstable); empty picks the host default"`
	Venus      *bool  `json:"gpu_venus,omitempty" jsonschema:"enable Vulkan-over-virtio (Venus) on the virtio-gpu-gl device; requires gpu_mode=virtio, a Venus-capable QEMU and a Linux guest with the Mesa vulkan-virtio ICD"`
	HostMem    string `json:"gpu_hostmem,omitempty" jsonschema:"host memory window for Venus blob resources, e.g. 8G; requires gpu_venus"`

	// Shares and SSH.
	VirtiofsDir string `json:"virtiofs_dir,omitempty" jsonschema:"host directory shared into the guest via virtiofs"`
	VirtiofsTag string `json:"virtiofs_tag,omitempty"`
	SSHKeyFile  string `json:"ssh_key_file,omitempty" jsonschema:"extra public key file to authorize in the guest"`
	SSHShare    *bool  `json:"ssh_share,omitempty" jsonschema:"enable host SSH-agent sharing via vsock"`
	Vsock       *bool  `json:"vsock,omitempty" jsonschema:"attach a virtio-vsock device for a private host-guest channel"`

	// passthrough template.
	NVMeDev  string `json:"nvme_dev,omitempty" jsonschema:"raw NVMe device for the passthrough template"`
	OVMFVars string `json:"ovmf_vars,omitempty" jsonschema:"OVMF vars file for the passthrough template"`

	// github-runner template.
	RunnerURL    string   `json:"runner_url,omitempty" jsonschema:"repo or org URL the runner registers with"`
	RunnerToken  string   `json:"runner_token,omitempty" jsonschema:"GitHub registration token; not needed when a credential snapshot exists for this name"`
	RunnerLabels []string `json:"runner_labels,omitempty" jsonschema:"runner labels; default self-hosted,linux,kvm"`
	RunnerSSHKey bool     `json:"runner_ssh_key,omitempty" jsonschema:"generate a per-instance GitHub SSH key instead of the shared global one"`

	// macos template.
	IPSW          string `json:"ipsw,omitempty" jsonschema:"latest (default), an https URL, or a local .ipsw path"`
	MacosvmDir    string `json:"macosvm_dir,omitempty" jsonschema:"import an existing macosvm bundle instead of restoring"`
	SkipFirstBoot bool   `json:"skip_first_boot,omitempty" jsonschema:"leave the restored macOS guest at Setup Assistant"`

	// torrent template.
	ShareMounts    []shareMountIn `json:"share_mounts,omitempty" jsonschema:"host directories shared into the torrent VM via virtiofs"`
	NFSMounts      []nfsMountIn   `json:"nfs_mounts,omitempty" jsonschema:"NFS exports mounted directly by the torrent guest; requires nic_mode=bridge"`
	NordVPNToken   string         `json:"nordvpn_token,omitempty" jsonschema:"NordVPN access token for the VPN kill-switch; the bitmagnet template exchanges it for a NordLynx WireGuard config"`
	NordVPNCountry string         `json:"nordvpn_country,omitempty" jsonschema:"NordVPN country to connect to, e.g. Netherlands; empty for auto"`
	WireGuardConf  string         `json:"wireguard_conf,omitempty" jsonschema:"path to a WireGuard config file for a generic VPN kill-switch"`

	// jellyfin template.
	Media        []string          `json:"media,omitempty" jsonschema:"media sources, same syntax as the CLI --media flag: hostdir:/path@/guest, nfs://server/export@/guest, smb://user@server/share@/guest, block:/dev/...@/guest, usb:VENDOR:PRODUCT@/guest"`
	MediaSecrets map[string]string `json:"media_secrets,omitempty" jsonschema:"secrets for media sources (e.g. SMB passwords), keyed by the prompt keys reported in a previous error"`

	// dns-sink template.
	DNSAdminUser         string `json:"dns_admin_user,omitempty" jsonschema:"AdGuard Home web UI username (default admin)"`
	DNSAdminPasswordHash string `json:"dns_admin_password_hash,omitempty" jsonschema:"bcrypt hash of the AdGuard Home web UI password; empty leaves the UI without a login (LAN-restricted by the guest firewall)"`

	// bitmagnet template. The kill-switch is configured through the shared
	// wireguard_conf field above.
	PGDataDir string `json:"pg_data_dir,omitempty" jsonschema:"absolute host directory bind-mounted over virtiofs as PostgreSQL's data directory, so the crawled index outlives the VM; empty keeps the database on the VM's own disk"`
}

type vmCreateOut struct {
	Name     string `json:"name"`
	Template string `json:"template"`
	Message  string `json:"message"`
	// RunnerPublicKey is set when a github-runner create generated a new SSH
	// key: add it to GitHub (account key or read-only deploy key).
	RunnerPublicKey string `json:"runner_public_key,omitempty"`
}

// templateExtras validates template-specific parameters and assembles the
// extras structs that the CLI collects via terminal prompts.
func (s *server) templateExtras(ctx context.Context, in vmCreateIn, opts *build.Opts) (runnerPubKey string, err error) {
	switch in.Template {
	case "passthrough":
		if in.NVMeDev == "" || in.OVMFVars == "" {
			return "", fmt.Errorf("the passthrough template requires nvme_dev and ovmf_vars")
		}

	case "torrent":
		extras := &build.TorrentExtras{}
		for _, m := range in.ShareMounts {
			extras.Mounts = append(extras.Mounts, templates.ShareMount{HostDir: m.HostDir, GuestPath: m.GuestPath})
		}
		for _, m := range in.NFSMounts {
			if m.Server == "" || !strings.HasPrefix(m.Export, "/") || !strings.HasPrefix(m.GuestPath, "/") {
				return "", fmt.Errorf("nfs mount %q:%q -> %q: server is required and export/guest_path must be absolute",
					m.Server, m.Export, m.GuestPath)
			}
			extras.NFSMounts = append(extras.NFSMounts, templates.NFSMount{
				Server:    m.Server,
				Export:    m.Export,
				GuestPath: m.GuestPath,
				Options:   m.Options,
			})
		}
		// The guest reaches the NFS server over the LAN, which user-mode NAT
		// cannot route.
		if len(extras.NFSMounts) > 0 && opts.NICMode == "user" {
			return "", fmt.Errorf("nfs_mounts requires nic_mode=bridge (user-mode NAT cannot reach an NFS server on the LAN)")
		}
		switch {
		case in.NordVPNToken != "":
			if err := vpn.ValidateToken(ctx, in.NordVPNToken); err != nil {
				return "", fmt.Errorf("validate NordVPN token: %w", err)
			}
			extras.NordConf = &vpn.NordVPNConfig{Token: in.NordVPNToken, Country: in.NordVPNCountry}
			extras.VPNProvider = "nordvpn"
		case in.WireGuardConf != "":
			content, readErr := os.ReadFile(in.WireGuardConf) //nolint:gosec // path supplied by the operating user via the MCP client
			if readErr != nil {
				return "", fmt.Errorf("read wireguard conf: %w", readErr)
			}
			wg, parseErr := vpn.ParseWireGuardConf(string(content))
			if parseErr != nil {
				return "", parseErr
			}
			extras.WireGuard = wg
			extras.VPNProvider = "generic"
		}
		opts.TorrentExtras = extras

	case "jellyfin":
		// mDNS + Jellyfin discovery don't work behind QEMU user-mode NAT.
		if in.NICMode != "bridge" {
			return "", fmt.Errorf("the jellyfin template requires nic_mode=bridge (mDNS + LAN discovery cannot traverse user-mode NAT)")
		}
		libs, parseErr := media.ParseSpecs(in.Media)
		if parseErr != nil {
			return "", parseErr
		}
		secrets := in.MediaSecrets
		if secrets == nil {
			secrets = map[string]string{}
		}
		// The CLI prompts for these on the terminal; over MCP the missing
		// keys are reported so the agent can retry with media_secrets set.
		var missing []string
		for _, src := range libs {
			_, prompts, planErr := src.Plan(media.Ubuntu, secrets)
			if planErr != nil {
				return "", fmt.Errorf("plan %s: %w", src.Kind, planErr)
			}
			for _, pp := range prompts {
				if _, ok := secrets[pp.Key]; !ok {
					missing = append(missing, fmt.Sprintf("%s (%s)", pp.Key, pp.Prompt))
				}
			}
		}
		if len(missing) > 0 {
			return "", fmt.Errorf("media sources need secrets; retry with media_secrets providing: %s", strings.Join(missing, ", "))
		}
		opts.JellyfinExtras = &build.JellyfinExtras{Libraries: libs, Secrets: secrets}

	case "dns-sink":
		// LAN clients must be able to reach the resolver, which user-mode NAT
		// cannot provide.
		if in.NICMode != "bridge" {
			return "", fmt.Errorf("the dns-sink template requires nic_mode=bridge (LAN clients cannot reach a resolver behind user-mode NAT)")
		}
		// Only a bcrypt hash is accepted: the MCP surface is non-interactive,
		// and a plaintext password would be logged in the tool-call transcript.
		if hash := in.DNSAdminPasswordHash; hash != "" && !strings.HasPrefix(hash, "$2") {
			return "", fmt.Errorf("dns_admin_password_hash must be a bcrypt hash (starting with $2a$/$2b$/$2y$), not a plaintext password")
		}
		opts.DNSSinkExtras = &build.DNSSinkExtras{
			AdminUser:    in.DNSAdminUser,
			PasswordHash: in.DNSAdminPasswordHash,
		}

	case "bitmagnet":
		extras := &build.BitmagnetExtras{}
		// The password is generated rather than accepted as a parameter: it is
		// never typed by a human, and a plaintext value passed here would be
		// recorded in the tool-call transcript.
		password, pwErr := templates.GeneratePGPassword()
		if pwErr != nil {
			return "", pwErr
		}
		extras.PGPassword = password

		switch {
		case in.WireGuardConf != "":
			content, readErr := os.ReadFile(in.WireGuardConf) //nolint:gosec // path supplied by the operating user via the MCP client
			if readErr != nil {
				return "", fmt.Errorf("read WireGuard config: %w", readErr)
			}
			wg, parseErr := vpn.ParseWireGuardConf(string(content))
			if parseErr != nil {
				return "", fmt.Errorf("parse WireGuard config: %w", parseErr)
			}
			extras.WireGuard = wg
			extras.VPNProvider = "wireguard"

		case in.NordVPNToken != "":
			// NordLynx is WireGuard, so a Nord account yields a complete
			// wg0.conf without the operator exporting one by hand.
			wg, fetchErr := vpn.NordLynxConfig(ctx, in.NordVPNToken, in.NordVPNCountry)
			if fetchErr != nil {
				return "", fetchErr
			}
			extras.WireGuard = wg
			extras.VPNProvider = "nordlynx"
		}

		if in.PGDataDir != "" {
			if !filepath.IsAbs(in.PGDataDir) {
				return "", fmt.Errorf("pg_data_dir must be an absolute host path, got %q", in.PGDataDir)
			}
			if mkErr := os.MkdirAll(in.PGDataDir, 0o700); mkErr != nil {
				return "", fmt.Errorf("create PostgreSQL data directory: %w", mkErr)
			}
			// The guest's postgres account is renumbered to the host owner of
			// the share; virtiofs will not let the guest chown it. See
			// templates.BitmagnetOptions.
			if fs := platform.NetworkFilesystemName(in.PGDataDir); fs != "" {
				return "", fmt.Errorf(
					"pg_data_dir %s is on %s; PostgreSQL cannot run its data directory over a network "+
						"filesystem (initdb would stall indefinitely). Use a directory on local storage",
					in.PGDataDir, fs)
			}
			uid, gid, statErr := dirOwner(in.PGDataDir)
			if statErr != nil {
				return "", statErr
			}
			extras.PGDataHostDir = in.PGDataDir
			extras.PGDataHostUID = uid
			extras.PGDataHostGID = gid
		}
		opts.BitmagnetExtras = extras

	case "github-runner":
		prepared, prepErr := runnersetup.Prepare(in.Name, in.RunnerURL, in.RunnerLabels, in.RunnerSSHKey, func() (string, error) {
			if in.RunnerToken == "" {
				return "", fmt.Errorf("runner_token is required (no credential snapshot exists for %q)", in.Name)
			}
			return in.RunnerToken, nil
		})
		if prepErr != nil {
			return "", prepErr
		}
		opts.RunnerExtras = prepared.Extras
		if prepared.KeyCreated {
			runnerPubKey = prepared.PubKey
		}

	case "macos":
		ipsw := in.IPSW
		if ipsw == "" {
			ipsw = "latest"
		}
		opts.MacOSExtras = &build.MacOSExtras{
			IPSW:          ipsw,
			MacosvmDir:    in.MacosvmDir,
			SkipFirstBoot: in.SkipFirstBoot,
		}
	}
	return runnerPubKey, nil
}

func (s *server) vmCreate(ctx context.Context, _ *mcp.CallToolRequest, in vmCreateIn) (*mcp.CallToolResult, vmCreateOut, error) {
	if !slices.Contains(build.KnownTemplates, in.Template) {
		return nil, vmCreateOut{}, fmt.Errorf("unknown template %q; see template_list", in.Template)
	}
	if _, err := s.mgr.LoadConfig(in.Name); err == nil {
		if !in.Reinstall {
			return nil, vmCreateOut{}, fmt.Errorf("VM %q already exists (pass reinstall=true to wipe and recreate it)", in.Name)
		}
		if state, serr := s.mgr.LoadState(in.Name); serr == nil && state.Running {
			if err := s.mgr.Stop(ctx, in.Name); err != nil {
				return nil, vmCreateOut{}, fmt.Errorf("stop VM before reinstall: %w", err)
			}
		}
		if err := s.mgr.Delete(in.Name); err != nil {
			return nil, vmCreateOut{}, fmt.Errorf("delete VM before reinstall: %w", err)
		}
	}

	opts := build.Opts{
		Name:          in.Name,
		Template:      in.Template,
		Memory:        in.Memory,
		CPUs:          in.CPUs,
		Distro:        in.Distro,
		DistroVersion: in.DistroVersion,
		NICMode:       in.NICMode,
		NICBridge:     in.NICBridge,
		NICMAC:        in.NICMAC,
		Disk:          in.Disk,
		DataDisks:     in.DataDisks,
		BootDisk:      in.BootDisk,
		BootDiskPath:  in.BootDiskPath,
		SPICEPort:     in.SPICEPort,
		Headless:      in.Headless,
		UEFI:          in.UEFI,
		GPUMode:       in.GPUMode,
		GPUPCI:        in.GPUPCI,
		GPUVendor:     in.GPUVendor,
		AntiDetect:    in.AntiDetect,
		GLBackend:     in.GLBackend,
		Venus:         in.Venus,
		HostMem:       in.HostMem,
		VirtiofsDir:   in.VirtiofsDir,
		VirtiofsTag:   in.VirtiofsTag,
		SSHKeyFile:    in.SSHKeyFile,
		SSHShare:      in.SSHShare,
		Vsock:         in.Vsock,
		SSHPort:       in.SSHPort,
		Hostname:      in.Hostname,
		User:          in.User,
		Password:      in.Password,
		NVMeDev:       in.NVMeDev,
		OVMFVars:      in.OVMFVars,
		Nested:        in.Nested,
		Emulate:       in.Emulate,
		NoAutoInstall: in.NoAutoInstall,
	}
	runnerPubKey, err := s.templateExtras(ctx, in, &opts)
	if err != nil {
		return nil, vmCreateOut{}, err
	}

	cfg, err := build.Build(ctx, s.prov, opts)
	if err != nil {
		return nil, vmCreateOut{}, err
	}
	if err := s.mgr.Create(ctx, cfg); err != nil {
		return nil, vmCreateOut{}, err
	}

	out := vmCreateOut{
		Name:            in.Name,
		Template:        cfg.Template,
		Message:         "created; boot with vm_start",
		RunnerPublicKey: runnerPubKey,
	}
	if in.Start {
		startOut, err := s.startAndWait(ctx, in.Name, defaultStartTimeout)
		if err != nil {
			out.Message = fmt.Sprintf("created, but starting it failed: %v", err)
			return nil, out, nil
		}
		out.Message = startOut.Message
	}
	return nil, out, nil
}

// ---- vm_start ----

const defaultStartTimeout = 5 * time.Minute

type vmStartIn struct {
	Name           string `json:"name" jsonschema:"name of the VM"`
	Wait           *bool  `json:"wait,omitempty" jsonschema:"wait for the guest to become ready (default true)"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"readiness wait timeout; default 300"`
	Recovery       bool   `json:"recovery,omitempty" jsonschema:"boot into the guest's recovery/rescue environment for this start (macOS recoveryOS on vz; systemd rescue target for direct-kernel Linux on vz); guests whose boot method has no launch-time hook boot normally and the message explains why"`
}

type vmStartOut struct {
	Name    string `json:"name"`
	Status  string `json:"status" jsonschema:"running, started (not waited), or install-complete"`
	IP      string `json:"ip,omitempty"`
	SSHPort int    `json:"ssh_port,omitempty"`
	Message string `json:"message,omitempty"`
}

func (s *server) vmStart(ctx context.Context, _ *mcp.CallToolRequest, in vmStartIn) (*mcp.CallToolResult, vmStartOut, error) {
	timeout := defaultStartTimeout
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}

	// `vee start --recovery` analog (issue #134): a supported plan starts the
	// guest into recovery and never waits for readiness — recovery
	// environments run no sshd, so the wait could only time out. An
	// unsupported plan boots normally and the message says why, rather than
	// silently reporting a recovery boot that did not happen.
	recoveryNote := ""
	if in.Recovery {
		cfg, err := s.mgr.LoadConfig(in.Name)
		if err != nil {
			return nil, vmStartOut{}, err
		}
		mode, note := vm.RecoveryPlan(cfg)
		if mode != vm.RecoveryUnsupported {
			if err := s.startDetached(ctx, in.Name, vm.WithRecovery()); err != nil {
				return nil, vmStartOut{}, err
			}
			return nil, vmStartOut{Name: in.Name, Status: "started", Message: note}, nil
		}
		recoveryNote = "recovery was not applied: " + note
	}

	if in.Wait != nil && !*in.Wait {
		if err := s.startDetached(ctx, in.Name); err != nil {
			return nil, vmStartOut{}, err
		}
		msg := "not waiting for readiness; poll with vm_status"
		if recoveryNote != "" {
			msg = recoveryNote + "; " + msg
		}
		return nil, vmStartOut{Name: in.Name, Status: "started", Message: msg}, nil
	}
	out, err := s.startAndWait(ctx, in.Name, timeout)
	if recoveryNote != "" && err == nil {
		if out.Message != "" {
			out.Message = recoveryNote + "; " + out.Message
		} else {
			out.Message = recoveryNote
		}
	}
	return nil, out, err
}

func (s *server) startDetached(ctx context.Context, name string, opts ...vm.StartOption) error {
	// Mirror `vee start`: make sure the pinned QEMU bundle exists before the
	// manager needs it. vz-backed VMs (macOS guests) don't use QEMU.
	if cfg, err := s.mgr.LoadConfig(name); err != nil || cfg.BackendName() == backend.QEMU {
		qemuPath, err := qemubin.Ensure()
		if err != nil {
			return fmt.Errorf("qemu binary: %w", err)
		}
		s.prov.Config().QemuBinaryPath = qemuPath
	}
	return s.mgr.Start(ctx, name, false, opts...)
}

func (s *server) startAndWait(ctx context.Context, name string, timeout time.Duration) (vmStartOut, error) {
	if err := s.startDetached(ctx, name); err != nil {
		return vmStartOut{}, err
	}

	// A first boot of a cloud-init template runs an install pass and powers
	// off when it finishes; there is nothing to wait for in that case.
	if state, err := s.mgr.LoadState(name); err == nil && !state.Running {
		return vmStartOut{
			Name:    name,
			Status:  "install-complete",
			Message: "install pass finished and the VM powered off; call vm_start again to boot it",
		}, nil
	}

	if err := s.mgr.WaitReady(ctx, name, timeout); err != nil {
		return vmStartOut{Name: name, Status: "started"},
			fmt.Errorf("VM started but readiness wait failed: %w", err)
	}

	out := vmStartOut{Name: name, Status: "running"}
	if cfg, state, err := s.loadVM(name); err == nil {
		out.SSHPort = state.SSHPort
		if ip, ipErr := resolveIP(ctx, cfg, state); ipErr == nil {
			out.IP = ip
		}
	}
	return out, nil
}

// ---- vm_stop ----

type vmStopIn struct {
	Name  string `json:"name" jsonschema:"name of the VM"`
	Force bool   `json:"force,omitempty" jsonschema:"kill the VM process instead of a graceful guest powerdown"`
}

type vmStopOut struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (s *server) vmStop(ctx context.Context, _ *mcp.CallToolRequest, in vmStopIn) (*mcp.CallToolResult, vmStopOut, error) {
	if in.Force {
		if err := s.mgr.ForceStop(ctx, in.Name); err != nil {
			return nil, vmStopOut{}, err
		}
		return nil, vmStopOut{Name: in.Name, Status: "killed"}, nil
	}
	if err := s.mgr.Stop(ctx, in.Name); err != nil {
		return nil, vmStopOut{}, err
	}
	return nil, vmStopOut{Name: in.Name, Status: "stopped"}, nil
}

// ---- vm_delete ----

type vmDeleteIn struct {
	Name  string   `json:"name,omitempty" jsonschema:"name of the VM to delete"`
	Names []string `json:"names,omitempty" jsonschema:"names of additional VMs to delete in the same call"`
}

type vmDeleteOut struct {
	Deleted []string `json:"deleted" jsonschema:"names of the VMs that were deleted"`
	Status  string   `json:"status"`
}

func (s *server) vmDelete(_ context.Context, _ *mcp.CallToolRequest, in vmDeleteIn) (*mcp.CallToolResult, vmDeleteOut, error) {
	var names []string
	if in.Name != "" {
		names = append(names, in.Name)
	}
	for _, n := range in.Names {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil, vmDeleteOut{}, fmt.Errorf("provide the VM to delete via name, or several via names")
	}

	var deleted []string
	var errs []error
	for _, name := range names {
		if state, err := s.mgr.LoadState(name); err == nil && state.Running {
			errs = append(errs, fmt.Errorf("VM %q is running; stop it with vm_stop before deleting", name))
			continue
		}
		if err := s.mgr.Delete(name); err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", name, err))
			continue
		}
		deleted = append(deleted, name)
	}
	if len(errs) > 0 {
		if len(deleted) > 0 {
			errs = append(errs, fmt.Errorf("deleted successfully: %s", strings.Join(deleted, ", ")))
		}
		return nil, vmDeleteOut{}, errors.Join(errs...)
	}
	return nil, vmDeleteOut{Deleted: deleted, Status: "deleted"}, nil
}

// ---- vm_ip ----

type vmIPOut struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

func (s *server) vmIP(ctx context.Context, _ *mcp.CallToolRequest, in vmNameIn) (*mcp.CallToolResult, vmIPOut, error) {
	cfg, state, err := s.loadRunningVM(in.Name)
	if err != nil {
		return nil, vmIPOut{}, err
	}
	ip, err := resolveIP(ctx, cfg, state)
	if err != nil {
		return nil, vmIPOut{}, err
	}
	return nil, vmIPOut{Name: in.Name, IP: ip}, nil
}

// resolveIP mirrors the CLI's resolution order: host lease/ARP tables by MAC
// first (works without a guest agent), then the QEMU guest agent.
func resolveIP(ctx context.Context, cfg *vm.VMConfig, state *vm.VMState) (string, error) {
	if cfg.NIC.MAC != "" {
		if ip, err := vm.ResolveIPFromMAC(cfg.NIC.MAC); err == nil {
			return ip, nil
		}
	}
	if state.QGASocket != "" {
		return vm.ResolveIPFromQGA(ctx, state.QGASocket)
	}
	return "", fmt.Errorf("cannot resolve the VM's IP: no host lease/ARP entry for its MAC and no guest agent socket")
}

// ---- vm_exec ----

const defaultExecTimeout = 60 * time.Second

type vmExecIn struct {
	Name           string `json:"name" jsonschema:"name of the VM"`
	Command        string `json:"command" jsonschema:"shell command; runs as /bin/sh -c <command> inside the guest"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"command timeout; default 60"`
}

type vmExecOut struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

func (s *server) vmExec(ctx context.Context, _ *mcp.CallToolRequest, in vmExecIn) (*mcp.CallToolResult, vmExecOut, error) {
	_, state, err := s.loadRunningVM(in.Name)
	if err != nil {
		return nil, vmExecOut{}, err
	}
	if state.QGASocket == "" {
		return nil, vmExecOut{}, fmt.Errorf(
			"VM %q has no guest agent socket; vm_exec needs a guest-agent-enabled template (macOS guests: use ssh instead)", in.Name)
	}

	timeout := defaultExecTimeout
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}

	client, err := qemu.NewQGAClient(ctx, state.QGASocket, 5*time.Second)
	if err != nil {
		return nil, vmExecOut{}, fmt.Errorf("connect guest agent: %w", err)
	}
	defer func() { _ = client.Close() }()

	// RunCommand polls the agent until the guest process exits, with no
	// deadline of its own; bound it so a hung guest command cannot wedge the
	// tool call. Closing the client unblocks the polling goroutine.
	type execResult struct {
		stdout, stderr string
		exitCode       int
		err            error
	}
	done := make(chan execResult, 1)
	go func() {
		stdout, stderr, exitCode, runErr := client.RunCommand("/bin/sh", []string{"-c", in.Command})
		done <- execResult{stdout, stderr, exitCode, runErr}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			return nil, vmExecOut{}, r.err
		}
		return nil, vmExecOut{Stdout: r.stdout, Stderr: r.stderr, ExitCode: r.exitCode}, nil
	case <-time.After(timeout):
		_ = client.Close()
		return nil, vmExecOut{}, fmt.Errorf("command timed out after %s (it may still be running in the guest)", timeout)
	case <-ctx.Done():
		_ = client.Close()
		return nil, vmExecOut{}, ctx.Err()
	}
}

// ---- vm_logs ----

const maxLogTailBytes = 512 * 1024

type vmLogsIn struct {
	Name    string `json:"name" jsonschema:"name of the VM"`
	Lines   int    `json:"lines,omitempty" jsonschema:"number of trailing lines to return; default 100"`
	Journal bool   `json:"journal,omitempty" jsonschema:"read the guest's forwarded systemd journal instead of the process log"`
	Kernel  bool   `json:"kernel,omitempty" jsonschema:"kernel messages only (implies journal)"`
}

type vmLogsOut struct {
	Path string `json:"path"`
	Log  string `json:"log"`
}

func (s *server) vmLogs(ctx context.Context, _ *mcp.CallToolRequest, in vmLogsIn) (*mcp.CallToolResult, vmLogsOut, error) {
	if _, err := s.mgr.LoadConfig(in.Name); err != nil {
		return nil, vmLogsOut{}, fmt.Errorf("VM %q not found: %w", in.Name, err)
	}
	lines := in.Lines
	if lines <= 0 {
		lines = 100
	}

	if in.Journal || in.Kernel {
		dir := filepath.Join(s.prov.Config().StoragePath, in.Name, "journal")
		var buf strings.Builder
		if err := journal.TailTo(ctx, &buf, dir, journal.TailOptions{
			KernelOnly: in.Kernel,
			Lines:      lines,
		}); err != nil {
			return nil, vmLogsOut{}, err
		}
		return nil, vmLogsOut{Path: dir, Log: buf.String()}, nil
	}

	logName := "qemu.log"
	if state, err := s.mgr.LoadState(in.Name); err == nil && state.BackendName() == backend.VZ {
		logName = vzhelper.LogFileName
	}
	logPath := filepath.Join(s.prov.Config().StoragePath, in.Name, logName)

	tail, err := tailFile(logPath, lines)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, vmLogsOut{}, fmt.Errorf("no log found for VM %q (has it been started?)", in.Name)
		}
		return nil, vmLogsOut{}, err
	}
	return nil, vmLogsOut{Path: logPath, Log: tail}, nil
}

// tailFile returns the last n lines of the file, reading at most
// maxLogTailBytes from its end.
func tailFile(path string, n int) (string, error) {
	f, err := os.Open(path) //nolint:gosec // path is derived from vee-managed storage and the VM name
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	offset := info.Size() - maxLogTailBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return "", err
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	all := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return strings.Join(all, "\n"), nil
}

// ---- shared helpers ----

func (s *server) loadVM(name string) (*vm.VMConfig, *vm.VMState, error) {
	cfg, err := s.mgr.LoadConfig(name)
	if err != nil {
		return nil, nil, fmt.Errorf("VM %q not found: %w", name, err)
	}
	state, err := s.mgr.LoadState(name)
	if err != nil {
		return nil, nil, fmt.Errorf("load state for %q: %w", name, err)
	}
	return cfg, state, nil
}

func (s *server) loadRunningVM(name string) (*vm.VMConfig, *vm.VMState, error) {
	cfg, state, err := s.loadVM(name)
	if err != nil {
		return nil, nil, err
	}
	if !state.Running {
		return nil, nil, fmt.Errorf("VM %q is not running", name)
	}
	return cfg, state, nil
}
