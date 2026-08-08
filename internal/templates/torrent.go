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

// NFSMount maps an NFS export to a guest mount point. Unlike ShareMount the
// guest mounts the export directly over the network, so the host does not need
// the export mounted itself.
type NFSMount struct {
	Server    string // NFS server host or IP, e.g. 192.168.178.76
	Export    string // export path on the server, e.g. /mnt/Data/Movies
	GuestPath string // absolute path inside the VM, e.g. /downloads/movies
	Options   string // mount options; defaults to nfsMountOptions when empty
}

// incompletePath is where qBittorrent writes in-progress torrents. It lives on
// the VM's own disk rather than under the save path so that the random small
// writes of an in-progress torrent never cross the network; the completed file
// is moved to the save path in one sequential pass.
const incompletePath = "/var/lib/qbittorrent/incomplete"

// nfsMountOptions are the default mount options for guest NFS mounts.
//
// hard (not soft) is deliberate: qBittorrent writes to these paths for the
// lifetime of a torrent, and a soft mount surfaces a NAS hiccup as EIO
// mid-write, which qBittorrent reports as a errored torrent rather than
// retrying. hard blocks until the server comes back instead.
const nfsMountOptions = "rw,hard,proto=tcp,timeo=600,retrans=2,_netdev"

// nfsServers returns the unique server addresses across mounts, preserving
// first-seen order so generated runcmds are deterministic.
func nfsServers(mounts []NFSMount) []string {
	seen := make(map[string]bool, len(mounts))
	var out []string
	for _, m := range mounts {
		if m.Server == "" || seen[m.Server] {
			continue
		}
		seen[m.Server] = true
		out = append(out, m.Server)
	}
	return out
}

// torrentBaseRunCmds returns the ufw rules that open the guest's own ports.
// VPN commands are inserted after these by the caller.
func torrentBaseRunCmds() []string {
	return []string{
		"ufw allow OpenSSH",
		"ufw allow 8080/tcp",
		"ufw --force enable",
	}
}

// torrentMountAndAppCmds returns the mount commands followed by the qBittorrent
// setup, in the order they must run.
//
// Ordering is the whole point of this function: the shares must be mounted
// before qBittorrent starts, or every download quietly lands on the VM's own
// disk. Callers must append the result, never prepend it.
//
// Note that chowns here name the user only, never "vee:vee" — cloudinit
// renders the account with no_user_group, so no vee group exists.
func torrentMountAndAppCmds(mounts []ShareMount, nfsMounts []NFSMount) []string {
	var cmds []string

	for i, m := range mounts {
		guestPath := m.GuestPath
		if guestPath == "" {
			guestPath = fmt.Sprintf("/share%d", i)
		}
		tag := virtiofsTagFor(m, i)
		// The fstab entry is what makes the mount survive a reboot: runcmds
		// only ever run on first boot, so without it the guest path silently
		// reverts to an empty local directory and qBittorrent writes into the
		// VM's own disk instead of the share.
		cmds = append(cmds,
			fmt.Sprintf("mkdir -p %s", guestPath),
			appendFstab(fstabEntry(tag, guestPath, "virtiofs", "defaults,nofail"), guestPath),
			fmt.Sprintf("mount -t virtiofs %s %s", tag, guestPath),
			fmt.Sprintf("chown vee %s", guestPath),
		)
	}

	for _, m := range nfsMounts {
		if m.GuestPath == "" {
			continue
		}
		opts := m.Options
		if opts == "" {
			opts = nfsMountOptions
		}
		source := fmt.Sprintf("%s:%s", m.Server, m.Export)
		// cloud-init's runcmd can fire before the NIC has an address on a
		// bridge that is slow to forward. Retry rather than fail silently: an
		// unmounted share sends every download to the VM's own disk, which is
		// exactly the failure this template exists to avoid.
		cmds = append(cmds,
			fmt.Sprintf("mkdir -p %s", m.GuestPath),
			appendFstab(fstabEntry(source, m.GuestPath, "nfs4", opts), m.GuestPath),
			fmt.Sprintf("for i in 1 2 3 4 5 6 7 8 9 10; do mount -t nfs4 -o %s %s %s && break || sleep 3; done",
				opts, source, m.GuestPath),
		)
	}

	return append(cmds,
		"mkdir -p /home/vee/.config/qBittorrent",
		"chown -R vee /home/vee/.config",
		// Incomplete torrents live on the VM's own disk, not on a share: the
		// random small writes of an in-progress torrent are pathological over
		// a network filesystem, and only the completed file is moved out.
		fmt.Sprintf("mkdir -p %s", incompletePath),
		fmt.Sprintf("chown -R vee %s", incompletePath),
		"systemctl enable --now qbittorrent-nox@vee",
	)
}

// virtiofsTagFor derives the virtiofs mount tag for a share.
func virtiofsTagFor(m ShareMount, i int) string {
	if m.GuestPath == "" {
		return fmt.Sprintf("share%d", i)
	}
	return strings.NewReplacer("/", "-", " ", "_").Replace(strings.TrimPrefix(m.GuestPath, "/"))
}

// nordVPNCmds returns the first-boot commands that install and connect the
// NordVPN snap. connectCmd is the final "nordvpn connect [country]".
//
// This sequence is order-sensitive and every step was verified by hand on a
// live guest.
//
// The snap ships its interfaces unconnected, and until they are connected
// every command fails with "Permission needed ... To start using the app, log
// in to your Nord Account by entering nordvpn login" — a message that reads
// like an authentication problem but is really about the missing permissions.
//
// "set analytics off" then has to precede the login. Without it the login
// prompts "Do you allow us to collect and use limited app performance data?
// (y/n)" on stdin, which cloud-init cannot answer: the read hits EOF
// immediately, nordvpn re-prompts, and the runcmd spins forever, writing
// hundreds of megabytes to the serial log and stalling cloud-init before it
// ever reaches the mounts. Disabling analytics records the consent up front
// and the login returns cleanly. It does not require an authenticated
// session, so running it before the login is not circular.
func nordVPNCmds(token, connectCmd string, nfsMounts []NFSMount) []string {
	cmds := []string{
		"snap install nordvpn",
		"snap connect nordvpn:network-control",
		"snap connect nordvpn:firewall-control",
		"snap connect nordvpn:network-observe",
		"snap connect nordvpn:system-observe",
		"snap connect nordvpn:hardware-observe",
		"snap connect nordvpn:login-session-observe",
		"snap connect nordvpn:network-manager",
		// The daemon needs a moment after the interfaces land before it will
		// accept a login.
		"for i in 1 2 3 4 5 6 7 8 9 10; do nordvpn status >/dev/null 2>&1 && break || sleep 3; done",
		"nordvpn set analytics off",
		fmt.Sprintf("nordvpn login --token %s", token),
		"nordvpn set technology nordlynx",
		"nordvpn set killswitch on",
		"nordvpn set autoconnect on",
	}
	// NordVPN's kill-switch is enforced inside the daemon rather than through
	// ufw, so the ufw holes elsewhere do not cover it and each NFS server must
	// also be whitelisted here — before the connection comes up, or the mounts
	// race the kill-switch.
	for _, server := range nfsServers(nfsMounts) {
		cmds = append(cmds, fmt.Sprintf("nordvpn whitelist add subnet %s/32", server))
	}
	// SSH is the only management path into a kill-switched guest: with the
	// kill-switch up the guest stops answering ARP on the LAN entirely, so
	// without this the VM is reachable only from its console. Port 22 alone,
	// never the qBittorrent port — vee tunnel forwards services over this SSH
	// connection (ssh -L), so nothing else needs to be exposed to the LAN.
	cmds = append(cmds, "nordvpn whitelist add port 22")
	return append(cmds, connectCmd)
}

// nfsBypassRules returns the ufw rules that let the guest reach each NFS
// server directly. NFS traffic always bypasses the VPN: the server sits on the
// LAN and is not reachable through the tunnel, so a default-deny outbound
// policy would otherwise block every mount. One rule per server rather than
// per mount, and per host rather than per subnet.
func nfsBypassRules(mounts []NFSMount) []string {
	servers := nfsServers(mounts)
	rules := make([]string, 0, len(servers))
	for _, server := range servers {
		rules = append(rules, fmt.Sprintf("ufw allow out to %s", server))
	}
	return rules
}

// fstabEntry renders a single /etc/fstab line.
func fstabEntry(source, target, fstype, options string) string {
	return fmt.Sprintf("%s %s %s %s 0 0", source, target, fstype, options)
}

// appendFstab returns a runcmd that appends line to /etc/fstab unless an entry
// for the same target already exists. Cloud-init runcmds only fire on first
// boot, so the mounts must be recorded in fstab to survive a reboot.
func appendFstab(line, target string) string {
	return fmt.Sprintf("grep -qs ' %s ' /etc/fstab || echo %q >> /etc/fstab", target, line)
}

// NewTorrentConfig returns a VMConfig for a lightweight torrent VM with SPICE display.
// spicePort defaults to 5934 if 0. sshKeys are injected into the VM's authorized_keys.
// mounts is an optional list of host→guest directory mappings shared via virtiofs.
// nfsMounts is an optional list of NFS exports mounted directly by the guest;
// these require a NIC mode that can reach the server (bridge, not user-mode NAT).
// nordConf, if non-nil, installs the nordvpn snap and connects via NordLynx on first boot.
// wgConf, if non-nil, injects a generic WireGuard config with a ufw kill-switch.
// vpnProvider records the provider name (e.g. "nordvpn", "generic") for display/monitoring.
// Only one of nordConf or wgConf should be set; nordConf takes precedence.
func NewTorrentConfig(ctx context.Context, p provider.Provider, name string, sshKeys []string, mounts []ShareMount, nfsMounts []NFSMount, nordConf *vpn.NordVPNConfig, wgConf *vpn.WireGuardConfig, vpnProvider string, spicePort int) (*vm.VMConfig, error) {
	conf := p.Config()
	// port 0 → manager assigns a random free port at create time
	_ = spicePort
	spicePort = 0

	img, err := images.NewImage(p, images.DistroUbuntu, "latest")
	if err != nil {
		return nil, fmt.Errorf("torrent image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("torrent image download: %w", err)
	}

	vmDir := filepath.Join(conf.StoragePath, name)

	// Pick the first mount's guest path as the default save path, or /downloads.
	// virtiofs mounts take precedence over NFS ones purely for backwards
	// compatibility with configs created before NFS was supported.
	savePath := "/downloads"
	switch {
	case len(mounts) > 0 && mounts[0].GuestPath != "":
		savePath = mounts[0].GuestPath
	case len(nfsMounts) > 0 && nfsMounts[0].GuestPath != "":
		savePath = nfsMounts[0].GuestPath
	}

	writeFiles := []vm.CloudInitWriteFile{
		{
			Path:        "/home/vee/.config/qBittorrent/qBittorrent.conf",
			Content:     qbittorrentConf(savePath, incompletePath),
			Permissions: "0600",
			// Owner is the user only: cloudinit renders the vee account with
			// no_user_group, so there is no "vee" group to chown to and
			// "vee:vee" fails the deferred write, which fails cloud-final.
			Owner: "vee",
			Defer: true,
		},
	}
	runCmds := torrentBaseRunCmds()

	packages := cloudinit.PackagesFor(cloudinit.Ubuntu, cloudinit.CategoryTorrent)

	switch {
	case nordConf != nil:
		// NordVPN snap approach: install snap, login with token, enable NordLynx kill-switch.
		connectCmd := "nordvpn connect"
		if nordConf.Country != "" {
			connectCmd = fmt.Sprintf("nordvpn connect %q", nordConf.Country)
		}
		runCmds = append(nordVPNCmds(nordConf.Token, connectCmd, nfsMounts), runCmds...)

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
		}
		// SSH is the only management path into a kill-switched guest, so its
		// replies have to survive the deny-outgoing policy above. The inbound
		// "ufw allow OpenSSH" in the base rules is not enough on its own: the
		// outbound half of an established SSH connection leaves on the LAN
		// interface, not wg0, and would otherwise be dropped. Everything else
		// stays inside the tunnel — vee tunnel forwards services over this SSH
		// connection rather than exposing their ports.
		wgCmds = append(wgCmds, "ufw allow out 22/tcp")
		// The NFS holes must be punched after the deny policy is set (ufw
		// evaluates in order, so an allow ahead of the policy is overridden)
		// but before wg-quick brings the tunnel up.
		wgCmds = append(wgCmds, nfsBypassRules(nfsMounts)...)
		wgCmds = append(wgCmds, "systemctl enable --now wg-quick@wg0")
		runCmds = append(wgCmds, runCmds...)
		packages = append(packages, "wireguard", "resolvconf")

	default:
		// No VPN. ufw's default outgoing policy is allow, so NFS already
		// works — but emit the rules anyway to pin the intent, so that
		// hardening the base policy later cannot silently break every mount.
		runCmds = append(nfsBypassRules(nfsMounts), runCmds...)
	}

	var virtiofsMounts []vm.VirtiofsMount
	for i, m := range mounts {
		virtiofsMounts = append(virtiofsMounts, vm.VirtiofsMount{
			SharedDir: m.HostDir,
			Tag:       virtiofsTagFor(m, i),
		})
	}
	if len(nfsMounts) > 0 {
		packages = append(packages, "nfs-common")
	}

	// Appended, never prepended: these must follow the users module and the
	// VPN commands above. See torrentMountAndAppCmds.
	runCmds = append(runCmds, torrentMountAndAppCmds(mounts, nfsMounts)...)

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
		Services: []vm.ServiceEntry{
			{Name: "spice", Port: 0, Protocol: vm.ServiceSPICE},
			{Name: "qbittorrent", Port: 8080, Protocol: vm.ServiceHTTP},
		},
		CreatedAt: time.Now(),
	}, nil
}
