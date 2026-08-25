package templates

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/internal/vpn"
	"github.com/Benehiko/vee/provider"
)

// torrentAlpineUser is the account qBittorrent runs as on the Alpine base.
//
// It is the cloud image's own default login user, deliberately, rather than a
// dedicated system account. virtiofs passes host ownership straight through to
// the guest and vee runs virtiofsd unprivileged, so the guest cannot chown a
// share — writes succeed only when the writing process's UID already matches
// the host owner of the directory. The Alpine cloud image gives this account
// UID 1000, which is the same UID the Ubuntu base's qBittorrent runs as and
// the usual owner of a host home directory, so shares that work on Ubuntu keep
// working here.
//
// A system account in the style of bitmagnet's would be tidier, and is the
// right shape for a daemon that only touches its own files. It is wrong here:
// it would land outside UID 1000 and every write to a virtiofs share would
// start failing with EPERM — silently, since qBittorrent reports it as an
// errored torrent rather than a permission problem.
const torrentAlpineUser = "alpine"

// torrentAlpineConfigDir is qBittorrent's profile directory on the Alpine base.
//
// The Ubuntu base relies on the packaged qbittorrent-nox@vee systemd unit to
// set HOME, which is what points qBittorrent at its configuration. OpenRC has
// no instance templating and no equivalent implicit HOME, so the profile
// directory is named explicitly via --profile instead.
const torrentAlpineConfigDir = "/home/" + torrentAlpineUser + "/.config"

// torrentAlpineRunCmds returns the ordered first-boot commands for the Alpine
// base.
//
// Ordering matters throughout: the network gate before anything that fetches,
// packages before the services that need them, the mounts before qBittorrent
// starts, and the kill-switch last of all — once the deny policy lands the
// guest can no longer reach the apk mirrors.
func torrentAlpineRunCmds(mounts []ShareMount, nfsMounts []NFSMount, wgConf *vpn.WireGuardConfig) []string {
	pkgs := []string{
		"ca-certificates", "curl", "iptables", "ip6tables",
		"qbittorrent-nox", "qemu-guest-agent",
	}
	if wgConf != nil {
		pkgs = append(pkgs, "wireguard-tools")
	}
	if len(nfsMounts) > 0 {
		// nfs-utils, not Debian's nfs-common: the mount helper for nfs4 lives
		// there, and without it "mount -t nfs4" fails with a bare "wrong fs
		// type" that reads like a kernel problem.
		pkgs = append(pkgs, "nfs-utils")
	}

	cmds := []string{
		// cloud-init's runcmd fires as soon as the networking service reports
		// started, which on Alpine is when dhcpcd has been launched — not when
		// it holds a lease. Without this gate the apk steps below run while the
		// guest still only has an IPv4LL (169.254.0.0/16) address and every
		// fetch fails, leaving the VM with no qBittorrent at all.
		"for i in $(seq 1 60); do ip -4 route show default | grep -qv '169.254' && break; sleep 2; done",

		// Alpine's cloud image ships only the main repo enabled; qbittorrent-nox,
		// wireguard-tools and qemu-guest-agent all live in community.
		`sed -i 's|^#\(.*/v[0-9.]*/community\)|\1|' /etc/apk/repositories`,
		// A default route is not proof the upstream mirror is reachable yet,
		// so both network-dependent steps retry rather than failing the boot.
		"for i in 1 2 3 4 5; do apk update && break; sleep 5; done",
		fmt.Sprintf("for i in 1 2 3 4 5; do apk add --no-cache %s && break; sleep 5; done",
			strings.Join(pkgs, " ")),
	}

	if len(nfsMounts) > 0 {
		// The fstab entries are mounted by the nfsmount service on later boots;
		// without it in the default runlevel a rebooted guest comes back with
		// empty mount points and every download lands on the VM's own disk.
		cmds = append(cmds, "rc-update add nfsmount default")
	}

	cmds = append(cmds,
		// vee tunnel and vee ssh resolve bridge-mode VM addresses over QGA.
		"rc-update add qemu-guest-agent default",
		"rc-service qemu-guest-agent start",

		// The Alpine cloud image ships "AllowTcpForwarding no", which makes
		// every ssh -L forward reset on connect. This template depends on
		// forwarding: with a kill-switch up the web UI is deliberately given no
		// firewall hole, so `vee tunnel` over SSH is the only way to reach it.
		//
		// Scoped to a drop-in rather than editing sshd_config in place, so the
		// image's own file stays pristine and the intent is greppable.
		"mkdir -p /etc/ssh/sshd_config.d",
		`printf 'AllowTcpForwarding yes\n' > /etc/ssh/sshd_config.d/10-vee-tunnel.conf`,
		// Older sshd builds ignore the drop-in directory unless it is included,
		// and Alpine's stock config does not always carry the Include line.
		`grep -qs '^Include /etc/ssh/sshd_config.d/\*.conf' /etc/ssh/sshd_config || `+
			`sed -i '1i Include /etc/ssh/sshd_config.d/*.conf' /etc/ssh/sshd_config`,
		// Validate before reloading: a bad config would leave sshd dead and the
		// guest unreachable, which on a kill-switched VM means unrecoverable.
		"sshd -t && rc-service sshd reload",
	)

	cmds = append(cmds, torrentAlpineMountCmds(mounts, nfsMounts)...)

	cmds = append(cmds,
		"rc-update add qbittorrent default",
		"rc-service qbittorrent start",
	)

	// Last: once the deny policy is in place the guest can no longer reach the
	// apk mirrors, so every network-dependent step above has to be done.
	return append(cmds, torrentAlpineKillSwitchCmds(wgConf, nfsMounts)...)
}

// torrentAlpineMountCmds mounts the shares and prepares qBittorrent's own
// directories.
//
// As on the Ubuntu base, the shares must be mounted before qBittorrent starts
// or every download quietly lands on the VM's own disk.
//
// Note the absence of a chown on the virtiofs mounts. The Ubuntu base emits one
// and it silently fails: virtiofsd runs unprivileged, so host ownership passes
// straight through and the guest cannot change it. Running qBittorrent as UID
// 1000 is what actually makes those writes work — see torrentAlpineUser.
func torrentAlpineMountCmds(mounts []ShareMount, nfsMounts []NFSMount) []string {
	var cmds []string

	for i, m := range mounts {
		guestPath := m.GuestPath
		if guestPath == "" {
			guestPath = fmt.Sprintf("/share%d", i)
		}
		tag := virtiofsTagFor(m, i)
		// The fstab entry is what makes the mount survive a reboot: runcmds
		// only ever run on first boot.
		cmds = append(cmds,
			fmt.Sprintf("mkdir -p %s", guestPath),
			appendFstab(fstabEntry(tag, guestPath, "virtiofs", "defaults,nofail"), guestPath),
			fmt.Sprintf("mount -t virtiofs %s %s", tag, guestPath),
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
		fmt.Sprintf("mkdir -p %s/qBittorrent", torrentAlpineConfigDir),
		fmt.Sprintf("chown -R %s:%s %s", torrentAlpineUser, torrentAlpineUser, torrentAlpineConfigDir),
		// Incomplete torrents live on the VM's own disk, not on a share: the
		// random small writes of an in-progress torrent are pathological over a
		// network filesystem, and only the completed file is moved out.
		fmt.Sprintf("mkdir -p %s", incompletePath),
		fmt.Sprintf("chown -R %s:%s %s", torrentAlpineUser, torrentAlpineUser, incompletePath),
	)
}

// torrentAlpineKillSwitchCmds returns the iptables rules that enforce the
// WireGuard kill-switch, and brings the tunnel up behind them.
//
// This is the iptables half of the ufw logic in torrentWGKillSwitchCmds; the
// two enforce the same policy and differ only in the firewall front-end. The
// policy is default-deny OUTPUT: if wg0 never comes up, or drops later,
// qBittorrent cannot fall back to the LAN interface and announce the host's
// real address to the swarm. It stops talking instead.
//
// With no WireGuard config the inbound rules are still applied — the web UI
// should not be exposed to the LAN regardless — but OUTPUT is left alone.
func torrentAlpineKillSwitchCmds(wgConf *vpn.WireGuardConfig, nfsMounts []NFSMount) []string {
	cmds := []string{
		// Inbound: SSH only. qBittorrent's web UI is deliberately absent from
		// this list — vee tunnel forwards it over the SSH connection, so it
		// never needs a hole of its own.
		"iptables -P INPUT DROP",
		"iptables -P FORWARD DROP",
		"iptables -A INPUT -i lo -j ACCEPT",
		"iptables -A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",
		"iptables -A INPUT -p tcp --dport 22 -j ACCEPT",
	}

	if wgConf != nil {
		cmds = append(cmds,
			// Resolve the endpoint here, before the deny policy lands, and pin
			// the handshake hole to those addresses. The config may name a
			// hostname, and once OUTPUT is DROP there is no DNS left to resolve
			// it with — so the lookup has to happen first and its result be
			// baked into the rules.
			wgResolveEndpointCmd(wgConf),

			"iptables -P OUTPUT DROP",
			"iptables -A OUTPUT -o lo -j ACCEPT",
			// Everything inside the tunnel. This is the only unrestricted
			// egress path, and it exists only while wg0 is up.
			"iptables -A OUTPUT -o wg0 -j ACCEPT",

			// The handshake is pinned to the endpoint's addresses and port
			// rather than opened to the whole internet on that port. An
			// unpinned "--dport 51820 ACCEPT" is a usable covert channel: any
			// process could reach any host that happens to listen there, with
			// the tunnel down.
			fmt.Sprintf("for ip in $(cat %s); do "+
				"iptables -A OUTPUT -d \"$ip\" -p udp --dport %d -j ACCEPT; done",
				wgEndpointAddrsFile, wireGuardEndpointPort(wgConf)),

			// SSH replies only on connections that are already established.
			// A bare "--sport 22 ACCEPT" lets any process bind source port 22
			// and open new connections to the internet with wg0 down; requiring
			// ESTABLISHED means the rule can only carry traffic belonging to a
			// session someone else initiated inbound.
			"iptables -A OUTPUT -p tcp --sport 22 -m conntrack --ctstate ESTABLISHED -j ACCEPT",

			// DHCP renewal, restricted to the broadcast address and the DHCP
			// source port. Losing the lease would take SSH down with it.
			"iptables -A OUTPUT -p udp --sport 68 --dport 67 -d 255.255.255.255 -j ACCEPT",
		)

		// NFS traffic always bypasses the VPN: the server sits on the LAN and
		// is not reachable through the tunnel, so a default-deny outbound
		// policy would otherwise block every mount.
		for _, server := range nfsServers(nfsMounts) {
			cmds = append(cmds, fmt.Sprintf("iptables -A OUTPUT -d %s -j ACCEPT", server))
		}

		cmds = append(cmds,
			// wg-quick, not the wireguard OpenRC service: Alpine's packaged
			// service reads /etc/conf.d/wireguard, and wg-quick applies the
			// Address/DNS/AllowedIPs directives our rendered wg0.conf carries.
			"wg-quick up wg0",
			// Cloud-init runcmds fire once, so the tunnel needs a boot-time
			// hook of its own or the guest comes back after a reboot with the
			// kill-switch up and no tunnel — reachable over SSH, downloading
			// nothing, which is the correct failure but a confusing one.
			//
			// Unlike the Ubuntu base this needs no retry timer: the hook runs
			// after the network is up, and re-running it is harmless.
			`printf '#!/bin/sh\nwg-quick up wg0\n' > /etc/local.d/wg0.start`,
			"chmod 0755 /etc/local.d/wg0.start",
			"rc-update add local default",
		)
	}

	return append(cmds,
		// iptables does not persist itself the way ufw does; without this the
		// kill-switch evaporates on reboot.
		"/etc/init.d/iptables save",
		"rc-update add iptables default",
	)
}

// torrentQbittorrentOpenRCService returns the OpenRC init script supervising
// qbittorrent-nox.
//
// --profile is explicit because OpenRC has no equivalent of the packaged
// qbittorrent-nox@vee systemd unit's implicit HOME, which is what points
// qBittorrent at its configuration on the Ubuntu base.
//
// supervise-daemon is deliberate: qbittorrent-nox exits if its save path is
// missing when it starts, and a dead torrent client is indistinguishable from
// an idle one until someone looks.
func torrentQbittorrentOpenRCService() string {
	return fmt.Sprintf(`#!/sbin/openrc-run

name="qbittorrent"
description="qBittorrent headless BitTorrent client"

command="/usr/bin/qbittorrent-nox"
command_args="--profile=%s"
command_user="%s:%s"
command_background="yes"
pidfile="/run/qbittorrent.pid"
output_log="/var/log/qbittorrent.log"
error_log="/var/log/qbittorrent.log"
supervisor="supervise-daemon"
respawn_delay=5
respawn_max=0

depend() {
	need net
	# The shares are fstab mounts, brought up by localmount/netmount rather than
	# by cloud-init on any boot after the first. Starting before them sends
	# every download to the VM's own disk.
	after firewall localmount netmount nfsmount
}

start_pre() {
	checkpath --directory --owner %s:%s --mode 0755 %s
	checkpath --file --owner %s:%s --mode 0644 /var/log/qbittorrent.log
}
`,
		torrentAlpineConfigDir,
		torrentAlpineUser, torrentAlpineUser,
		torrentAlpineUser, torrentAlpineUser, torrentAlpineConfigDir,
		torrentAlpineUser, torrentAlpineUser,
	)
}

// torrentAlpineWriteFiles returns the cloud-init files the Alpine base needs.
//
// The qBittorrent config is written root-owned and chowned in a runcmd rather
// than via cloud-init's Owner field: deferred-owner writes are unexercised on
// the Alpine base, and getting it wrong leaves qBittorrent unable to read its
// own configuration.
func torrentAlpineWriteFiles(savePath string, wgConf *vpn.WireGuardConfig) []vm.CloudInitWriteFile {
	files := []vm.CloudInitWriteFile{
		{
			Path:        torrentAlpineConfigDir + "/qBittorrent/qBittorrent.conf",
			Content:     qbittorrentConf(savePath, incompletePath),
			Permissions: "0600",
		},
		{
			Path:        "/etc/init.d/qbittorrent",
			Content:     torrentQbittorrentOpenRCService(),
			Permissions: "0755",
		},
	}
	if wgConf != nil {
		files = append(files, vm.CloudInitWriteFile{
			Path:        "/etc/wireguard/wg0.conf",
			Content:     vpn.RenderWireGuardConf(wgConf),
			Permissions: "0600",
		})
	}
	return files
}

// NewTorrentAlpineConfig returns a VMConfig for the torrent template on an
// Alpine base: qBittorrent under OpenRC, with the kill-switch enforced by
// iptables rather than ufw.
//
// This is the opt-in alternative to the default Ubuntu base, selected with
// --distro alpine. It buys a much smaller guest and the better-hardened
// kill-switch — the handshake hole and the SSH hole are both narrower than
// ufw's rule vocabulary can express — at three costs worth knowing:
//
//   - NordVPN is unavailable. It ships as a snap and Alpine has no snapd, so
//     only a generic WireGuard config can back the kill-switch here.
//   - x86_64 only. The Alpine cloud image vee downloads is a BIOS x86_64
//     image; the Ubuntu base runs on arm64 as well.
//   - No SPICE display. The guest is a headless daemon and the web UI is
//     reached over `vee tunnel`, so there is nothing to draw.
func NewTorrentAlpineConfig(
	ctx context.Context,
	p provider.Provider,
	name string,
	sshKeys []string,
	mounts []ShareMount,
	nfsMounts []NFSMount,
	nordConf *vpn.NordVPNConfig,
	wgConf *vpn.WireGuardConfig,
	vpnProvider string,
) (*vm.VMConfig, error) {
	if nordConf != nil {
		return nil, fmt.Errorf(
			"the alpine torrent base cannot use NordVPN: it installs as a snap and Alpine has no snapd. " +
				"Use --distro ubuntu for NordVPN, or supply a WireGuard config instead")
	}

	img, err := images.NewImage(p, images.DistroAlpine, "latest")
	if err != nil {
		return nil, fmt.Errorf("torrent image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("torrent image download: %w", err)
	}

	conf := p.Config()
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

	var virtiofsMounts []vm.VirtiofsMount
	for i, m := range mounts {
		virtiofsMounts = append(virtiofsMounts, vm.VirtiofsMount{
			SharedDir: m.HostDir,
			Tag:       virtiofsTagFor(m, i),
		})
	}

	return &vm.VMConfig{
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
		SSHPort:    deterministicSSHPort(name),
		GPU:        vm.GPUConfig{Mode: vm.GPUNone},
		Headless:   true,
		GuestAgent: true,
		// The Alpine nocloud image is a BIOS image; OVMF finds no bootable
		// device on it.
		UEFI:           vm.UEFIConfig{Enabled: false},
		VirtiofsMounts: virtiofsMounts,
		VPNProvider:    vpnProvider,
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
			Hostname: name,
			// No extra "vee" user: cloud-init emits `shell: /bin/bash` for it,
			// and the Alpine cloud image ships no bash, so useradd fails and
			// aborts the users_groups module before any authorized_keys are
			// written — locking SSH out of the guest entirely. The image's own
			// default user carries the SSH keys instead, and qBittorrent runs
			// as that account so virtiofs writes keep working (see
			// torrentAlpineUser).
			DefaultUser: images.DefaultUser(images.DistroAlpine),
			SSHKeys:     sshKeys,
			RunCmds:     torrentAlpineRunCmds(mounts, nfsMounts, wgConf),
			WriteFiles:  torrentAlpineWriteFiles(savePath, wgConf),
		},
		Services: []vm.ServiceEntry{
			{Name: "qbittorrent", Port: 8080, Protocol: vm.ServiceHTTP},
		},
		CreatedAt: time.Now(),
	}, nil
}
