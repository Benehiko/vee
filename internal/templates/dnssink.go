package templates

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

const (
	// DNSSinkDNSPort is the DNS port AdGuard Home serves inside the VM.
	DNSSinkDNSPort = 53
	// DNSSinkUIPort is the AdGuard Home web UI / admin API port.
	DNSSinkUIPort = 3000
	// DNSSinkAdGuardVersion pins the AdGuard Home release installed in the
	// guest. Pinning keeps VM creation reproducible; bump deliberately.
	DNSSinkAdGuardVersion = "v0.107.78"
)

// NewDNSSinkConfig returns a VMConfig for a minimal Alpine Linux VM running
// AdGuard Home as a network-wide DNS sinkhole for ad and malware domains.
//
// Bridge networking is required: the VM must hold a routable LAN address so
// that a router (or individual clients) can point their DNS resolver at it.
// QEMU user-mode NAT cannot serve DNS to other hosts on the network. Pass an
// empty bridge to use the host default ("br0").
//
// AdGuard Home is installed as a single static binary from the upstream GitHub
// release and supervised by OpenRC. The initial configuration is written by
// cloud-init so the VM comes up already blocking, with no setup wizard: DNS on
// 0.0.0.0:53, admin UI on 0.0.0.0:3000, DNS-over-TLS upstreams, and the
// AdGuard DNS filter plus StevenBlack unified hosts blocklists enabled.
//
// adminUser and adminPasswordHash configure the web UI login. The hash must be
// a bcrypt hash of the desired password; when empty, the UI is left
// unauthenticated and reachable only from the LAN, which is why the firewall
// restricts the UI port to RFC1918 sources.
func NewDNSSinkConfig(
	ctx context.Context,
	p provider.Provider,
	name string,
	sshKeys []string,
	bridge string,
	adminUser string,
	adminPasswordHash string,
) (*vm.VMConfig, error) {
	if bridge == "" {
		bridge = "br0"
	}
	if adminUser == "" {
		adminUser = "admin"
	}

	img, err := images.NewImage(p, images.DistroAlpine, "latest")
	if err != nil {
		return nil, fmt.Errorf("dns-sink image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("dns-sink image download: %w", err)
	}

	conf := p.Config()
	vmDir := filepath.Join(conf.StoragePath, name)

	adguardYAML := adguardHomeConfig(adminUser, adminPasswordHash)

	runCmds := []string{
		// cloud-init's runcmd fires as soon as the networking service reports
		// started, which on Alpine is when dhcpcd has been launched — not when
		// it holds a lease. On a bridge NIC the DHCP round trip goes out to the
		// LAN's real DHCP server, which routinely takes longer than that, so
		// without this gate the apk and curl steps below run while the guest
		// still only has an IPv4LL (169.254.0.0/16) address and every fetch
		// fails, leaving the VM with no AdGuard Home at all.
		"for i in $(seq 1 60); do ip -4 route show default | grep -qv '169.254' && break; sleep 2; done",

		// Alpine's cloud image ships only the main repo enabled; avahi,
		// qemu-guest-agent and the tooling below live in community.
		`sed -i 's|^#\(.*/v[0-9.]*/community\)|\1|' /etc/apk/repositories`,
		// A default route is not proof the upstream mirror is reachable yet,
		// so both network-dependent steps retry rather than failing the boot.
		"for i in 1 2 3 4 5; do apk update && break; sleep 5; done",
		"for i in 1 2 3 4 5; do apk add --no-cache ca-certificates curl tar iptables ip6tables avahi dbus qemu-guest-agent && break; sleep 5; done",

		// AdGuard Home ships as a static binary; no runtime deps to install.
		fmt.Sprintf(
			"for i in 1 2 3 4 5; do curl -fsSL --retry 3 --retry-delay 5 -o /tmp/AdGuardHome.tar.gz "+
				"https://github.com/AdguardTeam/AdGuardHome/releases/download/%s/AdGuardHome_linux_amd64.tar.gz "+
				"&& break; sleep 5; done",
			DNSSinkAdGuardVersion,
		),
		"tar -xzf /tmp/AdGuardHome.tar.gz -C /tmp",
		"install -m 0755 /tmp/AdGuardHome/AdGuardHome /usr/local/bin/AdGuardHome",
		"rm -rf /tmp/AdGuardHome /tmp/AdGuardHome.tar.gz",

		// Alpine's DNS resolver must not fight AdGuard Home for port 53.
		// The stock cloud image runs no local resolver, but be explicit so a
		// dhcpcd-supplied resolv.conf never points the guest at itself.
		`printf 'nameserver 1.1.1.1\nnameserver 9.9.9.9\n' > /etc/resolv.conf`,

		// OpenRC service (Alpine has no systemd, so the unit is hand-rolled).
		"chmod 0755 /etc/init.d/adguardhome",
		"rc-update add adguardhome default",
		"rc-service adguardhome start",

		// mDNS so the admin UI is reachable at http://<name>.local LAN-wide.
		"rc-update add dbus default",
		"rc-service dbus start",
		"rc-update add avahi-daemon default",
		"rc-service avahi-daemon start",

		// vee tunnel and vee ssh resolve bridge-mode VM addresses over QGA.
		"rc-update add qemu-guest-agent default",
		"rc-service qemu-guest-agent start",

		// Firewall: DNS open to the LAN, admin UI restricted to RFC1918,
		// everything else inbound dropped.
		"iptables -P INPUT DROP",
		"iptables -P FORWARD DROP",
		"iptables -A INPUT -i lo -j ACCEPT",
		"iptables -A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT",
		"iptables -A INPUT -p tcp --dport 22 -j ACCEPT",
		fmt.Sprintf("iptables -A INPUT -p udp --dport %d -j ACCEPT", DNSSinkDNSPort),
		fmt.Sprintf("iptables -A INPUT -p tcp --dport %d -j ACCEPT", DNSSinkDNSPort),
		"iptables -A INPUT -p udp --dport 5353 -j ACCEPT",
		fmt.Sprintf("iptables -A INPUT -p tcp -s 10.0.0.0/8 --dport %d -j ACCEPT", DNSSinkUIPort),
		fmt.Sprintf("iptables -A INPUT -p tcp -s 172.16.0.0/12 --dport %d -j ACCEPT", DNSSinkUIPort),
		fmt.Sprintf("iptables -A INPUT -p tcp -s 192.168.0.0/16 --dport %d -j ACCEPT", DNSSinkUIPort),
		"iptables -A INPUT -p icmp -j ACCEPT",
		"/etc/init.d/iptables save",
		"rc-update add iptables default",
	}

	cfg := &vm.VMConfig{
		Name:     name,
		Template: "dns-sink",
		Memory:   "512M",
		CPUs:     1,
		Sockets:  1,
		Cores:    1,
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
		// Alpine's nocloud cloud image is a BIOS image — booting it under
		// OVMF leaves the firmware with no bootable device.
		UEFI:     vm.UEFIConfig{Enabled: false},
		Hostname: name,
		SSHPort:  deterministicSSHPort(name),
		Disks: []vm.DiskConfig{
			{
				Path:        filepath.Join(vmDir, "storage", "disk-os.img"),
				Size:        "4G",
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
			// the docker template handles the same Alpine base.
			DefaultUser: images.DefaultUser(images.DistroAlpine),
			SSHKeys:     sshKeys,
			RunCmds:     runCmds,
			WriteFiles: []vm.CloudInitWriteFile{
				{
					Path:        "/opt/AdGuardHome/AdGuardHome.yaml",
					Content:     adguardYAML,
					Permissions: "0600",
				},
				{
					Path:        "/etc/init.d/adguardhome",
					Content:     adguardOpenRCService(),
					Permissions: "0755",
				},
			},
		},
		Services: []vm.ServiceEntry{
			{Name: "adguard", Port: DNSSinkUIPort, Protocol: vm.ServiceHTTP},
			{Name: "dns", Port: DNSSinkDNSPort, Protocol: vm.ServiceTCP},
		},
		CreatedAt: time.Now(),
	}

	return cfg, nil
}

// adguardOpenRCService returns the OpenRC init script supervising the
// AdGuard Home binary. AdGuard Home runs in the foreground under
// start-stop-daemon rather than using its own --service mode, which assumes
// systemd or a writable service manager on the host.
func adguardOpenRCService() string {
	return `#!/sbin/openrc-run

name="AdGuard Home"
description="Network-wide ads and trackers blocking DNS server"

command="/usr/local/bin/AdGuardHome"
command_args="--no-check-update --work-dir /opt/AdGuardHome --config /opt/AdGuardHome/AdGuardHome.yaml"
command_background="yes"
pidfile="/run/adguardhome.pid"
output_log="/var/log/adguardhome.log"
error_log="/var/log/adguardhome.log"

depend() {
	need net
	after firewall
}

start_pre() {
	checkpath --directory --mode 0755 /opt/AdGuardHome
	checkpath --file --mode 0644 /var/log/adguardhome.log
}
`
}

// adguardHomeConfig returns the initial AdGuardHome.yaml. Writing a complete
// configuration up front skips the first-run setup wizard, so the VM resolves
// and blocks from its very first boot.
//
// Upstreams are DNS-over-TLS (Cloudflare and Quad9) so that queries leaving
// the VM are encrypted; bootstrap resolvers are plain DNS purely to resolve
// the upstream hostnames themselves.
func adguardHomeConfig(adminUser, adminPasswordHash string) string {
	users := "users: []"
	if adminPasswordHash != "" {
		users = fmt.Sprintf("users:\n  - name: %s\n    password: %s", adminUser, adminPasswordHash)
	}

	return fmt.Sprintf(`http:
  address: 0.0.0.0:%d
  session_ttl: 720h
%s
auth_attempts: 5
block_auth_min: 15
language: en
theme: auto
dns:
  bind_hosts:
    - 0.0.0.0
  port: %d
  anonymize_client_ip: false
  ratelimit: 0
  refuse_any: true
  upstream_dns:
    - tls://1.1.1.1
    - tls://9.9.9.9
  bootstrap_dns:
    - 1.1.1.1
    - 9.9.9.9
  upstream_mode: load_balance
  fallback_dns: []
  cache_size: 8388608
  cache_ttl_min: 60
  cache_ttl_max: 86400
  cache_optimistic: true
  enable_dnssec: true
  filtering_enabled: true
  protection_enabled: true
  blocking_mode: default
  filters_update_interval: 24
  safebrowsing_enabled: true
  parental_enabled: false
  aaaa_disabled: false
  local_ptr_upstreams: []
filters:
  - enabled: true
    url: https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt
    name: AdGuard DNS filter
    id: 1
  - enabled: true
    url: https://adguardteam.github.io/HostlistsRegistry/assets/filter_2.txt
    name: AdAway Default Blocklist
    id: 2
  - enabled: true
    url: https://raw.githubusercontent.com/StevenBlack/hosts/master/hosts
    name: StevenBlack unified hosts
    id: 3
  - enabled: true
    url: https://adguardteam.github.io/HostlistsRegistry/assets/filter_9.txt
    name: The Big List of Hacked Malware Web Sites
    id: 4
  - enabled: true
    url: https://adguardteam.github.io/HostlistsRegistry/assets/filter_11.txt
    name: Malicious URL Blocklist (URLHaus)
    id: 5
whitelist_filters: []
user_rules: []
querylog:
  enabled: true
  interval: 168h
  size_memory: 1000
statistics:
  enabled: true
  interval: 720h
log:
  verbose: false
schema_version: 29
`, DNSSinkUIPort, users, DNSSinkDNSPort)
}
