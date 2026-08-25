package templates

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/internal/vpn"
	"github.com/Benehiko/vee/provider"
)

const (
	// BitmagnetWebPort is the port bitmagnet's web UI and GraphQL API listen
	// on inside the VM. It is deliberately never exposed to the LAN — the
	// firewall drops it and `vee tunnel` forwards it over SSH instead.
	BitmagnetWebPort = 3333

	// BitmagnetVersion pins the bitmagnet release installed in the guest.
	// Pinning keeps VM creation reproducible; bump deliberately.
	BitmagnetVersion = "v0.10.0"

	// BitmagnetPGDataDir is where PostgreSQL's data directory lives inside the
	// guest. When a host directory is bind-mounted this is the virtiofs mount
	// point; otherwise it is a plain directory on the VM's own disk.
	BitmagnetPGDataDir = "/var/lib/postgresql/data"

	// bitmagnetPGVirtiofsTag is the virtiofs tag used for the bind-mounted
	// PostgreSQL data directory.
	bitmagnetPGVirtiofsTag = "pgdata"

	// bitmagnetPGUser and bitmagnetPGDatabase name the role and database
	// bitmagnet connects to. The password is generated per-VM by the caller.
	bitmagnetPGUser     = "bitmagnet"
	bitmagnetPGDatabase = "bitmagnet"

	// bitmagnetConfigDir is bitmagnet's working directory, and therefore where
	// its configuration must live: bitmagnet locates config.yml relative to the
	// process's working directory (viper's "./config.yml"), and offers no flag
	// or environment variable to point it elsewhere.
	bitmagnetConfigDir = "/var/lib/bitmagnet"

	// bitmagnetDHTPort is the UDP port bitmagnet's DHT crawler listens on.
	// It must reach the internet through the VPN tunnel, never around it.
	bitmagnetDHTPort = 3334
)

// BitmagnetOptions carries the caller-supplied configuration for the bitmagnet
// template.
type BitmagnetOptions struct {
	// PGDataHostDir, when non-empty, is an absolute host directory shared into
	// the guest over virtiofs and used as PostgreSQL's data directory. This is
	// what lets the database outlive the VM: delete and recreate the guest and
	// the crawled torrent index is still there.
	//
	// When empty, PostgreSQL stores its data on the VM's own qcow2 disk and is
	// lost with the VM.
	PGDataHostDir string

	// PGDataHostUID and PGDataHostGID are the host owner of PGDataHostDir.
	//
	// They exist because virtiofs passes host ownership straight through to the
	// guest and vee runs virtiofsd unprivileged, so the guest cannot chown a
	// shared directory: `chown postgres` on the mount fails with EPERM, leaving
	// the data directory owned by a UID the guest's postgres user is not, and
	// initdb then refuses to touch it. Rather than fight the mapping, the guest's
	// postgres account is renumbered to these IDs so that it already owns the
	// share. Ignored when PGDataHostDir is empty.
	PGDataHostUID int
	PGDataHostGID int

	// PGPassword is the password for the bitmagnet PostgreSQL role. The
	// database only ever listens on loopback inside the guest, but a password
	// is set regardless so that a shell on the VM is not automatically a
	// database superuser session.
	PGPassword string

	// WireGuard, when non-nil, installs a wg0.conf and a default-deny
	// kill-switch so that no traffic can leave the guest outside the tunnel.
	// nil disables the VPN entirely, which for this template is a deliberate
	// and loudly-warned choice: bitmagnet's DHT crawler announces the guest's
	// IP address to the swarm.
	WireGuard *vpn.WireGuardConfig

	// VPNProvider records the provider name (e.g. "wireguard", "nordlynx")
	// for display and monitoring.
	VPNProvider string

	// Bridge is the host bridge to attach when using bridge networking. Empty
	// keeps the template's user-mode NAT default, which is sufficient here
	// because nothing on the LAN needs to reach the guest.
	Bridge string

	// DiskSize overrides the VM's own disk size. The database does not live
	// here when PGDataHostDir is set, so the default is modest.
	DiskSize string
}

// NewBitmagnetConfig returns a VMConfig for a minimal Alpine Linux VM running
// bitmagnet — a self-hosted BitTorrent DHT crawler and indexer — behind a
// WireGuard kill-switch.
//
// The VPN is the reason this template exists rather than a compose file on any
// existing host. bitmagnet crawls the DHT, which means it continuously
// announces itself to tens of thousands of peers; running it on a bare host
// publishes that host's IP address to the entire swarm. The kill-switch here
// is default-deny on OUTPUT, not merely a route through the tunnel: if wg0
// fails to come up, or drops later, the crawler cannot fall back to the LAN
// interface — it simply stops talking. Failing closed is the whole point.
//
// The web UI is never exposed. Port 3333 binds inside the guest and the
// firewall drops it from every source, so the only way in is `vee tunnel`
// over SSH. This mirrors the torrent template: with a kill-switch up, SSH is
// the single management path, and everything else rides over it.
//
// PostgreSQL runs alongside bitmagnet in the same guest rather than in a
// second VM — it is only ever reachable over loopback, so a network hop would
// buy nothing. Its data directory is bind-mounted from the host over virtiofs
// when opts.PGDataHostDir is set, which is what makes the crawled index
// survive `vee delete`.
func NewBitmagnetConfig(
	ctx context.Context,
	p provider.Provider,
	name string,
	sshKeys []string,
	opts BitmagnetOptions,
) (*vm.VMConfig, error) {
	if opts.PGPassword == "" {
		return nil, fmt.Errorf("bitmagnet template requires a PostgreSQL password")
	}

	img, err := images.NewImage(p, images.DistroAlpine, "latest")
	if err != nil {
		return nil, fmt.Errorf("bitmagnet image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("bitmagnet image download: %w", err)
	}

	conf := p.Config()
	vmDir := filepath.Join(conf.StoragePath, name)

	diskSize := opts.DiskSize
	if diskSize == "" {
		diskSize = "8G"
	}

	runCmds := bitmagnetRunCmds(opts)

	writeFiles := []vm.CloudInitWriteFile{
		{
			// 0600: this file carries the database password, since bitmagnet
			// reads its configuration from here rather than the environment.
			Path:        bitmagnetConfigDir + "/config.yml",
			Content:     bitmagnetConfig(opts.PGPassword, opts.WireGuard != nil),
			Permissions: "0600",
		},
		{
			Path:        "/etc/init.d/bitmagnet",
			Content:     bitmagnetOpenRCService(opts.WireGuard != nil),
			Permissions: "0755",
		},
	}

	var virtiofsMounts []vm.VirtiofsMount
	if opts.PGDataHostDir != "" {
		virtiofsMounts = append(virtiofsMounts, vm.VirtiofsMount{
			SharedDir: opts.PGDataHostDir,
			Tag:       bitmagnetPGVirtiofsTag,
		})
	}

	nic := vm.NICConfig{
		Mode:  "user",
		Model: "virtio-net-pci",
	}
	if opts.Bridge != "" {
		nic.Mode = "bridge"
		nic.Bridge = opts.Bridge
	}

	if opts.WireGuard != nil {
		writeFiles = append(writeFiles, vm.CloudInitWriteFile{
			Path:        "/etc/wireguard/wg0.conf",
			Content:     vpn.RenderWireGuardConf(opts.WireGuard),
			Permissions: "0600",
		})
		// The endpoint-refresh script, for a hostname endpoint that can rotate.
		// It arrives as a write-file rather than a runcmd because its content is
		// a multi-line shell program — see wgRefreshWriteFile.
		if wf := wgRefreshWriteFile(opts.WireGuard, alpineFirewallCmds(opts.WireGuard)); wf != nil {
			writeFiles = append(writeFiles, *wf)
		}
	}

	return &vm.VMConfig{
		Name:     name,
		Template: "bitmagnet",
		Memory:   "2G",
		CPUs:     2,
		Sockets:  1,
		Cores:    2,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC:      nic,
		GPU:      vm.GPUConfig{Mode: vm.GPUNone},
		Headless: true,
		// vee tunnel and vee ssh resolve bridge-mode VM addresses over QGA,
		// and with the kill-switch up SSH is the only way in at all.
		GuestAgent: true,
		// Alpine's nocloud cloud image is a BIOS image — booting it under
		// OVMF leaves the firmware with no bootable device.
		UEFI:           vm.UEFIConfig{Enabled: false},
		Hostname:       name,
		SSHPort:        deterministicSSHPort(name),
		VirtiofsMounts: virtiofsMounts,
		VPNProvider:    opts.VPNProvider,
		Disks: []vm.DiskConfig{
			{
				Path:        filepath.Join(vmDir, "storage", "disk-os.img"),
				Size:        diskSize,
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
			// default user (alpine) carries the SSH keys instead, matching how
			// the dns-sink and docker templates handle the same Alpine base.
			DefaultUser: images.DefaultUser(images.DistroAlpine),
			SSHKeys:     sshKeys,
			RunCmds:     runCmds,
			WriteFiles:  writeFiles,
		},
		Services: []vm.ServiceEntry{
			{Name: "bitmagnet", Port: BitmagnetWebPort, Protocol: vm.ServiceHTTP},
		},
		CreatedAt: time.Now(),
	}, nil
}

// bitmagnetRunCmds returns the ordered first-boot commands. Ordering matters
// throughout: packages before the database, the database before bitmagnet's
// migrations, and the kill-switch last of all — see bitmagnetKillSwitchCmds.
func bitmagnetRunCmds(opts BitmagnetOptions) []string {
	cmds := []string{
		// cloud-init's runcmd fires as soon as the networking service reports
		// started, which on Alpine is when dhcpcd has been launched — not when
		// it holds a lease. Without this gate the apk and curl steps below run
		// while the guest still only has an IPv4LL (169.254.0.0/16) address
		// and every fetch fails, leaving the VM with no bitmagnet at all.
		"for i in $(seq 1 60); do ip -4 route show default | grep -qv '169.254' && break; sleep 2; done",

		// Alpine's cloud image ships only the main repo enabled; postgresql,
		// wireguard-tools and qemu-guest-agent live in community.
		`sed -i 's|^#\(.*/v[0-9.]*/community\)|\1|' /etc/apk/repositories`,
		// A default route is not proof the upstream mirror is reachable yet,
		// so both network-dependent steps retry rather than failing the boot.
		"for i in 1 2 3 4 5; do apk update && break; sleep 5; done",
		"for i in 1 2 3 4 5; do apk add --no-cache ca-certificates curl tar iptables ip6tables " +
			"postgresql16 postgresql16-client postgresql16-contrib wireguard-tools qemu-guest-agent && break; sleep 5; done",
	}

	cmds = append(cmds, bitmagnetInstallCmds()...)
	cmds = append(cmds, bitmagnetPostgresCmds(opts)...)

	cmds = append(cmds,
		// bitmagnet only after PostgreSQL is accepting connections: its first
		// start runs schema migrations, and a failed migration leaves the
		// service dead rather than retrying.
		"rc-update add bitmagnet default",
		"rc-service bitmagnet start",

		// vee tunnel and vee ssh resolve bridge-mode VM addresses over QGA.
		"rc-update add qemu-guest-agent default",
		"rc-service qemu-guest-agent start",

		// The Alpine cloud image ships "AllowTcpForwarding no", which makes
		// every ssh -L forward reset on connect. This template depends on
		// forwarding: the web UI is deliberately given no firewall hole, so
		// `vee tunnel` over SSH is the only way to reach it. Without this the
		// UI is unreachable by any means.
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

	cmds = append(cmds, bitmagnetKillSwitchCmds(opts)...)
	return cmds
}

// bitmagnetInstallCmds fetches and installs the bitmagnet static binary from
// the upstream GitHub release, mirroring how dns-sink installs AdGuard Home.
func bitmagnetInstallCmds() []string {
	// The release assets use "x86_64" rather than Go's "amd64" spelling.
	url := fmt.Sprintf(
		"https://github.com/bitmagnet-io/bitmagnet/releases/download/%s/bitmagnet_%s_linux_x86_64.tar.gz",
		BitmagnetVersion, strings.TrimPrefix(BitmagnetVersion, "v"),
	)
	return []string{
		fmt.Sprintf(
			"for i in 1 2 3 4 5; do curl -fsSL --retry 3 --retry-delay 5 -o /tmp/bitmagnet.tar.gz %s && break; sleep 5; done",
			url,
		),
		"mkdir -p /tmp/bitmagnet",
		"tar -xzf /tmp/bitmagnet.tar.gz -C /tmp/bitmagnet",
		"install -m 0755 /tmp/bitmagnet/bitmagnet /usr/local/bin/bitmagnet",
		"rm -rf /tmp/bitmagnet /tmp/bitmagnet.tar.gz",
		// A dedicated unprivileged account: the crawler talks to the whole
		// internet, so it should not be root when it does.
		"addgroup -S bitmagnet 2>/dev/null || true",
		"adduser -S -D -H -G bitmagnet -s /sbin/nologin bitmagnet 2>/dev/null || true",
		// The service drops to the bitmagnet user, so it must be able to read
		// its own 0600 config.
		fmt.Sprintf("chown -R bitmagnet:bitmagnet %s", bitmagnetConfigDir),
	}
}

// bitmagnetPostgresCmds initialises PostgreSQL and creates bitmagnet's role
// and database.
//
// When a host directory is bind-mounted the data directory is a virtiofs
// mount. PostgreSQL is sensitive about its data directory in ways a normal
// application is not, and two of those matter here:
//
//   - Ownership and mode. initdb refuses to run unless the directory is owned
//     by the postgres user at mode 0700 — and on a virtiofs share the guest
//     cannot chown it, because virtiofs passes host ownership through and vee
//     runs virtiofsd unprivileged. The guest's postgres account is therefore
//     renumbered to the host owner instead of the directory being chowned.
//   - Existing data. initdb also refuses a non-empty directory, which is
//     exactly the case that matters most: re-creating the VM against a host
//     directory that already holds a crawled index must reuse it, not wipe
//     it. The guard below runs initdb only when PG_VERSION is absent.
func bitmagnetPostgresCmds(opts BitmagnetOptions) []string {
	var cmds []string

	if opts.PGDataHostDir != "" {
		// The fstab entry is what makes the mount survive a reboot: runcmds
		// only ever run on first boot, so without it the data directory
		// silently reverts to an empty local directory and PostgreSQL
		// initialises a second, empty database on the VM's own disk while the
		// real index sits untouched on the host.
		cmds = append(cmds,
			fmt.Sprintf("mkdir -p %s", BitmagnetPGDataDir),
			appendFstab(
				fstabEntry(bitmagnetPGVirtiofsTag, BitmagnetPGDataDir, "virtiofs", "defaults,nofail"),
				BitmagnetPGDataDir,
			),
			fmt.Sprintf("mount -t virtiofs %s %s", bitmagnetPGVirtiofsTag, BitmagnetPGDataDir),
		)
	} else {
		cmds = append(cmds, fmt.Sprintf("mkdir -p %s", BitmagnetPGDataDir))
	}

	if opts.PGDataHostDir != "" {
		// Renumber the guest's postgres account to the host owner of the share
		// rather than chowning the share to the guest's postgres account: the
		// latter is impossible. virtiofs presents host ownership as-is and vee
		// runs virtiofsd unprivileged, so chown on the mount fails with EPERM
		// and initdb then refuses the directory it cannot access.
		//
		// The remaining local files postgres owns (its home, run and log
		// directories) are chowned to the new IDs afterwards, since usermod
		// does not follow them outside the home directory.
		// The target IDs are usually 1000:1000, which the Alpine cloud image has
		// already given to its own "alpine" login user — and usermod refuses to
		// duplicate a UID, so postgres must not simply be pointed at it. Move
		// the occupant aside first, then take the IDs.
		//
		// Renumbering the login user is safe here: SSH authenticates by key, not
		// by UID, and its home directory is re-chowned below. It is also the
		// only account that could collide, since Alpine's system users all sit
		// below 1000.
		// The delimiter in "cut -d ':'" is quoted deliberately. cloud-init emits
		// each single-line runcmd as a bare YAML scalar, and an unquoted "-d:"
		// makes the parser read the whole command as a mapping key — the same
		// class of silent breakage as a command starting with "[". See
		// TestBitmagnetUserDataIsValidYAML.
		const vacatedUID = 1500
		cmds = append(cmds,
			fmt.Sprintf("if getent passwd %d >/dev/null; then "+
				"occupant=$(getent passwd %d | cut -d ':' -f1); "+
				"usermod -u %d \"$occupant\"; "+
				"chown -R %d \"/home/$occupant\" 2>/dev/null || true; fi",
				opts.PGDataHostUID, opts.PGDataHostUID, vacatedUID, vacatedUID),
			fmt.Sprintf("if getent group %d >/dev/null; then "+
				"groupmod -g %d \"$(getent group %d | cut -d ':' -f1)\"; fi",
				opts.PGDataHostGID, vacatedUID, opts.PGDataHostGID),
			fmt.Sprintf("groupmod -g %d postgres", opts.PGDataHostGID),
			fmt.Sprintf("usermod -u %d -g %d postgres", opts.PGDataHostUID, opts.PGDataHostGID),
			// usermod only follows the account's home directory, so the run and
			// log directories postgres owns have to be re-chowned by hand.
			fmt.Sprintf("chown -R %d:%d /var/lib/postgresql /run/postgresql /var/log/postgresql 2>/dev/null || true",
				opts.PGDataHostUID, opts.PGDataHostGID),
		)
	} else {
		cmds = append(cmds, fmt.Sprintf("chown postgres:postgres %s", BitmagnetPGDataDir))
	}

	cmds = append(cmds,
		fmt.Sprintf("chmod 0700 %s", BitmagnetPGDataDir),
		// Point Alpine's init script at our data directory rather than its
		// packaged default, so `rc-service postgresql` and initdb agree.
		fmt.Sprintf("printf 'data_dir=\"%s\"\\n' > /etc/conf.d/postgresql", BitmagnetPGDataDir),
		// Reuse an existing cluster when one is already on the bind-mount;
		// only initialise when there is genuinely nothing there.
		// "test -f", not "[ -f ... ]": cloud-init renders each single-line
		// runcmd as a bare YAML scalar, and a scalar beginning with "[" parses
		// as a flow sequence. That does not fail loudly — it makes the whole
		// user-data document invalid, so cloud-init discards every module and
		// the guest boots with no packages, no SSH keys and no services at all.
		fmt.Sprintf(
			"test -f %s/PG_VERSION || su postgres -c '/usr/libexec/postgresql16/initdb --auth-local=peer --auth-host=scram-sha-256 -D %s'",
			BitmagnetPGDataDir, BitmagnetPGDataDir,
		),
		// The database is never reachable off-box: bitmagnet runs in this same
		// guest, so loopback is the entire surface it needs.
		fmt.Sprintf("printf \"listen_addresses = '127.0.0.1'\\n\" >> %s/postgresql.conf", BitmagnetPGDataDir),
		"rc-update add postgresql default",
		"rc-service postgresql start",
		// initdb finishing is not the same as the postmaster accepting
		// connections, and the role creation below fails outright if it runs
		// too early.
		"for i in $(seq 1 30); do su postgres -c 'pg_isready -q' && break; sleep 2; done",
	)

	// Both statements are idempotent so that re-creating the VM against an
	// existing bind-mounted cluster succeeds instead of erroring on a role
	// and database that are already there.
	cmds = append(cmds,
		fmt.Sprintf(
			"su postgres -c \"psql -tAc \\\"SELECT 1 FROM pg_roles WHERE rolname='%s'\\\"\" | grep -q 1 || "+
				"su postgres -c \"psql -c \\\"CREATE ROLE %s LOGIN PASSWORD '%s'\\\"\"",
			bitmagnetPGUser, bitmagnetPGUser, opts.PGPassword,
		),
		fmt.Sprintf(
			"su postgres -c \"psql -tAc \\\"SELECT 1 FROM pg_database WHERE datname='%s'\\\"\" | grep -q 1 || "+
				"su postgres -c \"createdb -O %s %s\"",
			bitmagnetPGDatabase, bitmagnetPGUser, bitmagnetPGDatabase,
		),
	)

	return cmds
}

// bitmagnetKillSwitchCmds returns the firewall rules, and brings up the
// WireGuard tunnel behind them.
//
// These run last, after every network-dependent step above. That ordering is
// deliberate: the policy is default-deny OUTPUT, so once it is in place the
// guest can no longer reach apk mirrors or GitHub except through wg0. Setting
// it earlier would leave the install steps racing the tunnel.
//
// The kill-switch is what makes this template safe to run. bitmagnet's DHT
// crawler announces the guest to the swarm continuously; if wg0 never comes up
// or drops later, default-deny means the crawler falls silent rather than
// falling back to the LAN interface and publishing the host's real address.
func bitmagnetKillSwitchCmds(opts BitmagnetOptions) []string {
	cmds := []string{
		// Inbound: SSH only. The bitmagnet web UI is deliberately absent from
		// this list — vee tunnel forwards it over the SSH connection, so it
		// never needs a hole of its own.
		"iptables -P INPUT DROP",
		"iptables -P FORWARD DROP",
		"iptables -A INPUT -i lo -j ACCEPT",
		"iptables -A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",
		"iptables -A INPUT -p tcp --dport 22 -j ACCEPT",
	}

	if opts.WireGuard != nil {
		endpointPort := wireGuardEndpointPort(opts.WireGuard)

		cmds = append(cmds,
			// Resolve the endpoint here, before the deny policy lands, and pin
			// the handshake hole to those addresses. The config may name a
			// hostname, and once OUTPUT is DROP there is no DNS left to resolve
			// it with — so the lookup has to happen first and its result be
			// baked into the rules.
			//
			// Shared with the torrent bases rather than spelled out again: the
			// refresh script re-resolves into the same file, so the two have to
			// agree on its path and format or a refresh would write somewhere
			// the boot-time rules never read.
			wgResolveEndpointCmd(opts.WireGuard),

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
			fmt.Sprintf("for ip in $(cat /etc/wireguard/endpoint-addrs); do "+
				"iptables -A OUTPUT -d \"$ip\" -p udp --dport %d -j ACCEPT; done", endpointPort),

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

		// Belt and braces for the application itself: even if one of the
		// narrow holes above is ever widened by mistake, the crawler's own
		// traffic is rejected unless it is inside the tunnel. The rule sits
		// after the "-o wg0 ACCEPT" above, so tunnelled traffic is unaffected.
		cmds = append(cmds,
			"iptables -A OUTPUT ! -o wg0 -m owner --gid-owner bitmagnet -j REJECT",
		)

		cmds = append(cmds,
			"rc-update add wireguard default 2>/dev/null || true",
			// wg-quick, not the wireguard OpenRC service: Alpine's packaged
			// service reads /etc/conf.d/wireguard, and wg-quick applies the
			// Address/DNS/AllowedIPs directives our rendered wg0.conf carries.
			"wg-quick up wg0",
		)

		// Re-pin the handshake hole when the endpoint rotates. Without this the
		// hole stays pinned to the addresses resolved once at build time: a
		// provider that re-addresses its server leaves wg-quick dialling the new
		// IP while the firewall still permits only the old one, so the handshake
		// is dropped and the tunnel never comes back. It fails closed — the
		// crawler falls silent rather than leaking — but it is a silent
		// permanent outage, and it survives reboots.
		//
		// Returns nothing for a literal-IP endpoint, which cannot rotate; the
		// plain boot hook below covers that case.
		refresh := alpineWGEndpointRefreshCmds(opts.WireGuard)
		cmds = append(cmds, refresh...)

		if len(refresh) == 0 {
			// Cloud-init runcmds fire once, so the tunnel needs a boot-time
			// hook of its own or the guest comes back after a reboot with the
			// kill-switch up and no tunnel — reachable over SSH, crawling
			// nothing, which is the correct failure but a confusing one.
			//
			// Only for a literal-IP endpoint: the refresh wiring above installs
			// its own /etc/local.d/wg0.start, which runs the re-resolve ahead of
			// "wg-quick up". Writing this one unconditionally would overwrite
			// that and take the refresh back out on every boot.
			cmds = append(cmds,
				`printf '#!/bin/sh\nwg-quick up wg0\n' > /etc/local.d/wg0.start`,
				"chmod 0755 /etc/local.d/wg0.start",
			)
		}

		cmds = append(cmds, "rc-update add local default")
	}

	return append(cmds,
		"/etc/init.d/iptables save",
		"rc-update add iptables default",
	)
}

// bitmagnetConfig returns the initial /etc/bitmagnet/config.yml.
//
// The HTTP server binds all interfaces inside the guest, which is safe only
// because the firewall drops the port from every source — reaching the UI
// means `vee tunnel`, which arrives over loopback via SSH. Binding loopback
// directly would work too, but breaks the moment someone forwards the port
// with anything other than an SSH tunnel.
//
// The database credentials live here rather than in an OpenRC environment
// file. /etc/conf.d values are shell variables sourced by the init script, not
// exported into the service's environment, so bitmagnet never saw them and
// silently fell back to its defaults — connecting as "postgres" with no
// password. Because the file therefore holds a secret, it is written 0600 and
// owned by the bitmagnet user.
//
// ssl_mode is explicit for the same reason it has to be: bitmagnet defaults to
// requiring TLS, and a local PostgreSQL with no certificate refuses the
// connection outright. The connection never leaves loopback inside a single
// guest, so disabling TLS costs nothing here.
//
// crawlEnabled is false when no VPN is configured. Crawling the DHT announces
// the guest's address to every peer it contacts, so with no tunnel to hide
// behind the crawler stays off and the VM comes up as an indexer with an empty
// index rather than quietly publishing the host's real IP to the swarm. The
// rest of the stack — database, web UI, API — runs normally, so adding a
// tunnel later is the only step needed to start crawling.
func bitmagnetConfig(pgPassword string, crawlEnabled bool) string {
	return fmt.Sprintf(`http_server:
  local_address: ":%d"
dht_crawler:
  enabled: %t
dht_server:
  port: %d
postgres:
  host: 127.0.0.1
  port: 5432
  name: %s
  user: %s
  password: %q
  ssl_mode: disable
log:
  level: info
`, BitmagnetWebPort, crawlEnabled, bitmagnetDHTPort, bitmagnetPGDatabase, bitmagnetPGUser, pgPassword)
}

// bitmagnetOpenRCService returns the OpenRC init script supervising bitmagnet.
// Alpine has no systemd, so the unit is hand-rolled, matching how the dns-sink
// template supervises AdGuard Home.
func bitmagnetOpenRCService(crawlEnabled bool) string {
	// The worker keys must agree with dht_crawler.enabled: starting the
	// dht_crawler worker is what actually puts the guest on the DHT, so leaving
	// it in the argument list would defeat the config switch.
	keys := "--keys=http_server --keys=queue_server"
	if crawlEnabled {
		keys += " --keys=dht_crawler"
	}
	return fmt.Sprintf(`#!/sbin/openrc-run

name="bitmagnet"
description="Self-hosted BitTorrent DHT crawler and indexer"

command="/usr/local/bin/bitmagnet"
command_args="worker run %s"
command_user="bitmagnet:bitmagnet"
command_background="yes"
pidfile="/run/bitmagnet.pid"
output_log="/var/log/bitmagnet.log"
error_log="/var/log/bitmagnet.log"
# bitmagnet reads ./config.yml relative to its working directory and has no
# flag or environment variable to override that, so the directory is the
# configuration mechanism.
directory="%s"
# The credentials are deliberately NOT exported here. bitmagnet also reads
# POSTGRES_* from the environment, but this init script has to stay executable
# by OpenRC (0755), so a password embedded in it would be world-readable —
# strictly worse than the 0600 config.yml in the working directory above.
# Restart rather than stay dead: bitmagnet panics and exits if PostgreSQL is
# not ready when it starts, and a crawler that gave up hours ago looks
# identical to one that is merely idle.
supervisor="supervise-daemon"
respawn_delay=5
respawn_max=0

depend() {
	need net
	after firewall postgresql
}

start_pre() {
	checkpath --file --mode 0644 --owner bitmagnet:bitmagnet /var/log/bitmagnet.log
}
`, keys, bitmagnetConfigDir)
}

// GeneratePGPassword returns a random password for the bitmagnet database
// role. It is generated rather than supplied by the operator: nobody ever
// types it — bitmagnet reads it from its environment file inside the guest,
// and the database never listens off-loopback — so prompting for one would
// only invite a weak, reused password. On the MCP surface it also keeps a
// plaintext password out of the tool-call transcript.
//
// base64url of 24 random bytes keeps the result shell-safe in the psql and
// OpenRC files it is interpolated into.
func GeneratePGPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate database password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
