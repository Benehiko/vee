package templates

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Benehiko/vee/internal/cloudinit"
	"github.com/Benehiko/vee/internal/vpn"
)

// TestAlpineKillSwitchDeniesByDefault is the security-critical test for the
// Alpine base. The template exists so that torrent traffic cannot escape the
// tunnel: if the OUTPUT policy is ever anything but DROP, qBittorrent announces
// the guest's real address to the swarm the moment wg0 drops.
func TestAlpineKillSwitchDeniesByDefault(t *testing.T) {
	cmds := torrentAlpineKillSwitchCmds(testWGConf(), nil)

	for _, want := range []string{
		"iptables -P OUTPUT DROP",
		"iptables -P INPUT DROP",
		"iptables -A OUTPUT -o wg0 -j ACCEPT",
		"wg-quick up wg0",
	} {
		if indexOf(cmds, want) < 0 {
			t.Errorf("kill-switch rules missing %q", want)
		}
	}

	// The tunnel must come up after the deny policy is installed, or there is
	// a window where traffic leaves unprotected.
	deny := indexOf(cmds, "iptables -P OUTPUT DROP")
	tunnel := indexOf(cmds, "wg-quick up wg0")
	if deny > tunnel {
		t.Errorf("tunnel comes up (index %d) before the deny policy (index %d); traffic could leak in between",
			tunnel, deny)
	}
}

// TestAlpineHandshakeHoleIsPinned mirrors the Ubuntu base's test. The handshake
// must be able to leave, and the hole that lets it leave must be pinned to the
// endpoint's own addresses — an unpinned "--dport 51820 ACCEPT" would be a
// usable covert channel with the tunnel down.
func TestAlpineHandshakeHoleIsPinned(t *testing.T) {
	cmds := torrentAlpineKillSwitchCmds(testWGConf(), nil)
	joined := strings.Join(cmds, "\n")

	if !strings.Contains(joined, "getent ahostsv4") {
		t.Error("the endpoint must be resolved before the deny policy; there is no DNS left afterwards")
	}
	if !strings.Contains(joined, "-p udp --dport 51820 -j ACCEPT") {
		t.Error("no handshake hole: the tunnel can never be established from behind the kill-switch")
	}

	for _, c := range cmds {
		if !strings.Contains(c, "--dport 51820") {
			continue
		}
		if !strings.Contains(c, wgEndpointAddrsFile) {
			t.Errorf("handshake hole is not pinned to the endpoint addresses: %q", c)
		}
	}

	resolve, deny := -1, -1
	for i, c := range cmds {
		switch {
		// The build-time resolve, not the endpoint-refresh script: that
		// script embeds the same command text but is installed after the
		// deny policy on purpose, since it runs later with a DNS hole of
		// its own.
		case strings.Contains(c, "getent ahostsv4") && !strings.Contains(c, wgRefreshScriptPath):
			resolve = i
		case c == "iptables -P OUTPUT DROP":
			deny = i
		}
	}
	if resolve < 0 || deny < 0 {
		t.Fatalf("expected both a resolve step and a deny policy, got %v", cmds)
	}
	if resolve > deny {
		t.Error("the endpoint is resolved after the deny policy lands; DNS would already be blocked")
	}
}

// TestAlpineKillSwitchPersistsAcrossReboot covers the reboot path. cloud-init
// runcmds fire once, so both the tunnel and the iptables rules need their own
// boot-time persistence or the guest comes back either unprotected or with the
// kill-switch up and no tunnel.
func TestAlpineKillSwitchPersistsAcrossReboot(t *testing.T) {
	joined := strings.Join(torrentAlpineKillSwitchCmds(testWGConf(), nil), "\n")

	if !strings.Contains(joined, "/etc/local.d/wg0.start") {
		t.Error("no boot hook: a rebooted guest comes back kill-switched with no tunnel")
	}
	if !strings.Contains(joined, "rc-update add local default") {
		t.Error("the local runlevel must be enabled or the boot hook never fires")
	}
	if !strings.Contains(joined, "/etc/init.d/iptables save") {
		t.Error("iptables rules are not saved; the kill-switch evaporates on reboot")
	}
	if !strings.Contains(joined, "rc-update add iptables default") {
		t.Error("the iptables service must be enabled or the saved rules are never restored")
	}
}

// TestAlpineWebUINeverExposed pins the access model. With a kill-switch up SSH
// is the only management path, and vee tunnel forwards the web UI over it — so
// port 8080 must never get a firewall hole of its own.
func TestAlpineWebUINeverExposed(t *testing.T) {
	joined := strings.Join(torrentAlpineKillSwitchCmds(testWGConf(), nil), "\n")

	if strings.Contains(joined, "8080") {
		t.Error("qBittorrent's port must stay closed: vee tunnel forwards it over SSH")
	}
	if !strings.Contains(joined, "iptables -A INPUT -p tcp --dport 22 -j ACCEPT") {
		t.Error("SSH is the only way into a kill-switched guest and must be allowed inbound")
	}
}

// TestAlpineNFSBypassesTunnel covers the NFS exception. The server sits on the
// LAN and is not reachable through the tunnel, so a default-deny outbound
// policy would otherwise block every mount.
func TestAlpineNFSBypassesTunnel(t *testing.T) {
	mounts := []NFSMount{
		{Server: "192.168.178.76", Export: "/mnt/Data/Movies", GuestPath: "/downloads/movies"},
		{Server: "192.168.178.76", Export: "/mnt/Data/Shows", GuestPath: "/downloads/shows"},
	}
	cmds := torrentAlpineKillSwitchCmds(testWGConf(), mounts)

	rule := "iptables -A OUTPUT -d 192.168.178.76 -j ACCEPT"
	allow := indexOf(cmds, rule)
	if allow < 0 {
		t.Fatalf("no NFS bypass rule; every mount would be blocked. got %v", cmds)
	}
	// One rule per server, not per mount.
	var count int
	for _, c := range cmds {
		if c == rule {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected one bypass rule per server, got %d", count)
	}

	if tunnel := indexOf(cmds, "wg-quick up wg0"); allow > tunnel {
		t.Error("the NFS hole must exist before the tunnel comes up, or the mount races the kill-switch")
	}
}

// TestAlpineRunsQbittorrentAsUID1000User is the ownership regression test.
//
// virtiofs passes host ownership straight through and virtiofsd runs
// unprivileged, so the guest cannot chown a share: writes succeed only when the
// writing process already has the host owner's UID. The Alpine cloud image
// gives its default login user UID 1000, which is what the Ubuntu base's
// qBittorrent runs as. A dedicated system account in bitmagnet's style would
// land outside 1000 and every write to a share would fail with EPERM —
// silently, reported as an errored torrent rather than a permissions problem.
func TestAlpineRunsQbittorrentAsUID1000User(t *testing.T) {
	if torrentAlpineUser != "alpine" {
		t.Fatalf("qBittorrent must run as the image's default UID-1000 user, got %q", torrentAlpineUser)
	}

	svc := torrentQbittorrentOpenRCService()
	if !strings.Contains(svc, `command_user="alpine:alpine"`) {
		t.Error("the service must drop to the UID-1000 user, or virtiofs writes fail with EPERM")
	}

	// The Ubuntu base chowns its virtiofs mounts and the chown silently fails.
	// Emitting one here would imply the ownership problem is handled when it is
	// not — the UID match is what actually makes the writes work.
	for _, c := range torrentAlpineMountCmds([]ShareMount{{HostDir: "/tank/movies", GuestPath: "/downloads"}}, nil) {
		if strings.HasPrefix(c, "chown") && strings.Contains(c, "/downloads") {
			t.Errorf("virtiofs mounts must not be chowned; the guest cannot change host ownership: %q", c)
		}
	}
}

// TestAlpineServiceStartsAfterMounts pins the ordering constraint a real first
// boot exposed on the Ubuntu base: starting qBittorrent before the shares are
// mounted sends downloads to the VM's own disk instead of the NAS.
//
// On Alpine this has to hold twice — once through the runcmd order on first
// boot, and once through the service's own depend() on every boot after, since
// cloud-init does not run again.
func TestAlpineServiceStartsAfterMounts(t *testing.T) {
	nfs := []NFSMount{{Server: "192.168.178.76", Export: "/mnt/Data", GuestPath: "/downloads"}}
	cmds := torrentAlpineRunCmds(nil, nfs, nil)

	mount := -1
	for i, c := range cmds {
		if strings.Contains(c, "mount -t nfs4") {
			mount = i
		}
	}
	start := indexOf(cmds, "rc-service qbittorrent start")
	if mount < 0 || start < 0 {
		t.Fatalf("expected both a mount and a service start, got %v", cmds)
	}
	if mount > start {
		t.Error("qBittorrent starts before the shares are mounted; downloads would land on the VM's own disk")
	}

	// Later boots have no cloud-init, so the service itself must wait.
	svc := torrentQbittorrentOpenRCService()
	if !strings.Contains(svc, "after firewall localmount netmount nfsmount") {
		t.Error("the service must depend on the mount services, or a reboot races the fstab mounts")
	}
}

// TestAlpineInstallsBeforeKillSwitch pins the ordering the deny policy forces:
// once OUTPUT is DROP the guest cannot reach the apk mirrors at all, so every
// package must already be installed.
func TestAlpineInstallsBeforeKillSwitch(t *testing.T) {
	cmds := torrentAlpineRunCmds(nil, nil, testWGConf())

	apk, deny := -1, -1
	for i, c := range cmds {
		switch {
		case strings.Contains(c, "apk add"):
			apk = i
		case c == "iptables -P OUTPUT DROP":
			deny = i
		}
	}
	if apk < 0 || deny < 0 {
		t.Fatalf("expected both an apk install and a deny policy, got %v", cmds)
	}
	if apk > deny {
		t.Error("packages are installed after the deny policy; the mirrors would already be unreachable")
	}

	// The network gate has to come first, or apk runs on an IPv4LL address.
	if !strings.Contains(cmds[0], "169.254") {
		t.Errorf("first runcmd must gate on a real DHCP lease, got %q", cmds[0])
	}
	// qbittorrent-nox lives in the community repo, which the cloud image masks.
	if !strings.Contains(strings.Join(cmds, "\n"), "/community") {
		t.Error("the community repo must be enabled or qbittorrent-nox is not found")
	}
}

// TestAlpineNFSUsesAlpinePackageName guards a silent breakage: nfs-common is
// the Debian name, and apk would fail to find it, leaving the guest unable to
// mount anything.
func TestAlpineNFSUsesAlpinePackageName(t *testing.T) {
	joined := strings.Join(torrentAlpineRunCmds(nil, []NFSMount{{Server: "10.0.0.1"}}, nil), "\n")

	if strings.Contains(joined, "nfs-common") {
		t.Error("nfs-common is the Debian package name; Alpine needs nfs-utils")
	}
	if !strings.Contains(joined, "nfs-utils") {
		t.Error("NFS mounts need nfs-utils for the nfs4 mount helper")
	}
	if !strings.Contains(joined, "rc-update add nfsmount default") {
		t.Error("without nfsmount in the default runlevel a reboot comes back with empty mount points")
	}
}

// TestAlpineRejectsBothVPNs guards the one genuinely ambiguous input. A
// NordVPN token and a WireGuard config are two ways to describe the same
// tunnel, and silently preferring one would give the guest a VPN the caller did
// not ask for.
//
// Note there is deliberately no test that NordVPN is rejected outright: the
// NordVPN *client* is a snap and Alpine has no snapd, but NordLynx is
// WireGuard, so a token is exchanged for a wg0.conf and backs the same
// kill-switch. That exchange needs the network, so it is covered by the e2e
// test rather than here.
func TestAlpineRejectsBothVPNs(t *testing.T) {
	_, err := NewTorrentAlpineConfig(
		t.Context(), nil, "dl", nil, nil, nil,
		&vpn.NordVPNConfig{Token: "tok"}, testWGConf(), "nordvpn",
	)
	if err == nil {
		t.Fatal("passing both a NordVPN token and a WireGuard config must be rejected")
	}
	if !strings.Contains(err.Error(), "not both") {
		t.Errorf("the error should name the conflict, got %q", err)
	}
}

// TestAlpineUserDataIsValidYAML renders the Alpine base's cloud-init exactly as
// vm.Manager does and parses it.
//
// cloud-init renders each single-line runcmd as a bare YAML scalar, so a
// command starting with "[" parses as a flow sequence and invalidates the whole
// document. cloud-init then discards every module: the guest boots with no
// packages, no SSH keys and no services, and the only evidence is one "Failed
// loading yaml blob" line in the serial log.
func TestAlpineUserDataIsValidYAML(t *testing.T) {
	mounts := []ShareMount{{HostDir: "/tank/movies", GuestPath: "/downloads"}}
	nfs := []NFSMount{{Server: "192.168.178.76", Export: "/mnt/Data", GuestPath: "/downloads/nas"}}

	wfs := torrentAlpineWriteFiles("/downloads", testWGConf())
	files := make([]cloudinit.WriteFile, 0, len(wfs))
	for _, f := range wfs {
		files = append(files, cloudinit.WriteFile{
			Path:        f.Path,
			Content:     f.Content,
			Permissions: f.Permissions,
		})
	}

	rendered, err := cloudinit.RenderUserData(&cloudinit.Config{
		Hostname:    "dl",
		DefaultUser: "alpine",
		SSHKeys:     []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAtest"},
		RunCmds:     torrentAlpineRunCmds(mounts, nfs, testWGConf()),
		WriteFiles:  files,
	})
	if err != nil {
		t.Fatalf("render user-data: %v", err)
	}

	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(rendered), &parsed); err != nil {
		t.Fatalf("rendered user-data is not valid YAML (cloud-init would discard every module): %v", err)
	}

	cmds, ok := parsed["runcmd"].([]any)
	if !ok {
		t.Fatal("rendered user-data has no runcmd list")
	}
	for i, c := range cmds {
		if _, ok := c.(string); !ok {
			t.Errorf("runcmd[%d] parsed as %T, not a string: %v", i, c, c)
		}
	}

	// No users: block — cloud-init emits `shell: /bin/bash` for a custom user
	// and Alpine has no bash, which aborts the module before authorized_keys
	// are written and locks SSH out of the guest entirely.
	if _, hasUsers := parsed["users"]; hasUsers {
		t.Error("the Alpine base must not declare a users: block; useradd fails without bash and SSH is lost")
	}
}

// TestAlpineWGRefreshWiringIsSingleLine guards the runcmd rendering. Every
// runcmd must be one line: cloud-init renders them as YAML scalars, and an
// embedded literal newline turns the document into something the parser reads
// as a mapping and discards wholesale — taking every module with it.
//
// The trap is specific. These entries build shell scripts whose content is
// "#!/bin/sh\n...", and a Go interpreted string literal turns that \n into a
// real newline before printf ever sees it. It has to stay a backslash-n so the
// escape reaches printf in the guest.
func TestAlpineWGRefreshWiringIsSingleLine(t *testing.T) {
	cfg := testWGConf()
	for _, c := range alpineWGEndpointRefreshCmds(cfg) {
		if strings.Contains(c, "\n") {
			t.Errorf("runcmd contains a literal newline and would break the cloud-init "+
				"document; use a raw string so \\n reaches printf: %q", c)
		}
	}
}

// TestAlpineWGRefreshOnlyForHostnames mirrors the Ubuntu guard: a literal-IP
// endpoint cannot rotate, so it gets none of the refresh machinery.
func TestAlpineWGRefreshOnlyForHostnames(t *testing.T) {
	literal := &vpn.WireGuardConfig{
		PrivateKey: "k", PublicKey: "p", Endpoint: "192.0.2.10:51820",
	}
	if got := alpineWGEndpointRefreshCmds(literal); got != nil {
		t.Errorf("a literal-IP endpoint needs no refresh machinery, got %v", got)
	}
	if got := alpineWGEndpointRefreshCmds(testWGConf()); got == nil {
		t.Error("a hostname endpoint must get the refresh machinery")
	}
}

// TestAlpineWGRefreshPersistsRules covers the Alpine-specific half of the fix.
// iptables does not persist itself, so a re-pinned hole that is never saved is
// lost on the next boot — dropping the guest straight back into the outage the
// refresh exists to end.
func TestAlpineWGRefreshPersistsRules(t *testing.T) {
	cfg := testWGConf()
	script := wgRefreshEndpointScript(cfg, alpineFirewallCmds(cfg))

	if !strings.Contains(script, "/etc/init.d/iptables save") {
		t.Error("the re-pinned rules are never saved; they would not survive a reboot")
	}
	// The refresh must run before the tunnel comes up at boot, not after: a
	// handshake attempted against a stale hole just fails.
	wiring := strings.Join(alpineWGEndpointRefreshCmds(cfg), "\n")
	hook := strings.Index(wiring, "/etc/local.d/wg0.start")
	if hook < 0 {
		t.Fatal("the boot hook must be rewritten to run the refresh")
	}
	line := wiring[strings.LastIndex(wiring[:hook], "printf"):hook]
	refresh := strings.Index(line, wgRefreshScriptPath)
	up := strings.Index(line, "wg-quick up wg0")
	if refresh < 0 || up < 0 {
		t.Fatalf("the boot hook must both refresh and bring the tunnel up: %q", line)
	}
	if refresh > up {
		t.Error("the boot hook brings the tunnel up before refreshing the endpoint; " +
			"the handshake would be attempted against a stale hole")
	}
}

// TestAlpineWGRefreshStartsCrond guards an assumption that was wrong the first
// time. Dropping a script into /etc/periodic/<n> only runs if something runs
// run-parts on it, and the Alpine cloud image ships crond stopped and out of
// the default runlevel — so the periodic refresh silently never fired and a
// rotation was only ever picked up by a reboot. Verified on a live guest:
// "rc-service crond status" reported "stopped" while the script sat in place,
// executable and never run.
//
// The stock /etc/crontabs/root has run-parts entries for 15min and coarser, but
// none for 1min, so the entry has to be added as well as the service enabled.
func TestAlpineWGRefreshStartsCrond(t *testing.T) {
	joined := strings.Join(alpineWGEndpointRefreshCmds(testWGConf()), "\n")

	if !strings.Contains(joined, "rc-update add crond default") {
		t.Error("crond is not enabled: the Alpine image ships it stopped, so the periodic " +
			"refresh would never run and a rotation would need a reboot to be noticed")
	}
	if !strings.Contains(joined, "rc-service crond") {
		t.Error("crond is not started in this boot: the refresh would not run until the " +
			"guest is next rebooted, which is exactly the case it exists to avoid")
	}

	// A directory with no run-parts entry is a directory nothing reads.
	dir := "/etc/periodic/1min"
	if strings.Contains(joined, dir) && !strings.Contains(joined, "run-parts "+dir) {
		t.Errorf("%s has no run-parts crontab entry; the stock crontab does not cover it", dir)
	}
}

// TestAlpineIPv6IsDropped covers the family the kill-switch used to ignore
// entirely. The image installs ip6tables but nothing ever gave it a rule, so
// its policy stayed at the default ACCEPT: on a network advertising IPv6, a
// guest could reach the internet over IPv6 on the LAN interface while every
// IPv4 path was correctly denied — the announce leaking the host's real
// address just as effectively as an IPv4 one would.
func TestAlpineIPv6IsDropped(t *testing.T) {
	// Both paths: the tunnel does not carry IPv6 either way, so the drop is not
	// conditional on a WireGuard config.
	for _, tc := range []struct {
		name string
		conf *vpn.WireGuardConfig
	}{
		{"with wireguard", testWGConf()},
		{"without wireguard", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmds := torrentAlpineKillSwitchCmds(tc.conf, nil)

			for _, want := range []string{
				"ip6tables -P INPUT DROP",
				"ip6tables -P OUTPUT DROP",
				"ip6tables -P FORWARD DROP",
			} {
				if indexOf(cmds, want) < 0 {
					t.Errorf("IPv6 is not denied by default: missing %q", want)
				}
			}

			// A distro-shipped ACCEPT rule would sit ahead of the policy and
			// defeat it, so the chains are flushed — and that has to happen
			// before the loopback exceptions are added, or it removes them.
			flush := indexOf(cmds, "ip6tables -F")
			if flush < 0 {
				t.Fatal("IPv6 chains are never flushed; a shipped ACCEPT rule would survive")
			}
			lo := indexOf(cmds, "ip6tables -A OUTPUT -o lo -j ACCEPT")
			if lo < 0 {
				t.Fatal("no IPv6 loopback exception; software binding ::1 would fail confusingly")
			}
			if flush > lo {
				t.Error("the IPv6 flush runs after the loopback exception and would remove it")
			}
		})
	}
}

// TestAlpineIPv6PolicyPersists is the reboot half. Alpine's iptables service
// saves the two families separately, so the IPv4 save does not carry IPv6 with
// it: without an explicit ip6tables save the guest comes back with the IPv4
// kill-switch intact and IPv6 wide open again.
func TestAlpineIPv6PolicyPersists(t *testing.T) {
	cmds := torrentAlpineKillSwitchCmds(testWGConf(), nil)

	if indexOf(cmds, "/etc/init.d/ip6tables save") < 0 {
		t.Error("IPv6 rules are never saved; the policy is lost on reboot")
	}
	if indexOf(cmds, "rc-update add ip6tables default") < 0 {
		t.Error("the ip6tables service is not enabled; saved IPv6 rules would not be restored")
	}

	// Saving before the rules are installed would persist an empty table.
	save := indexOf(cmds, "/etc/init.d/ip6tables save")
	if drop := indexOf(cmds, "ip6tables -P OUTPUT DROP"); drop > save {
		t.Error("the IPv6 policy is saved before it is installed; an empty table would persist")
	}
}

// TestAlpineConfWrittenWhereProfileReads pins the config path against
// qBittorrent's --profile semantics.
//
// --profile names a profile *root*: qBittorrent appends "qBittorrent/config/"
// to it and reads the .conf from there. The Alpine base used to write the file
// one level up, at "<profile>/qBittorrent/qBittorrent.conf", where qBittorrent
// never looked. It then started against an empty profile, so
// WebUI\LocalHostAuth=false never applied and it fell back to password auth —
// answering every tunnelled request with 401 and printing a temporary admin
// password into /var/log/qbittorrent.log.
//
// The Ubuntu base is unaffected: the packaged qbittorrent-nox@vee systemd unit
// points qBittorrent at its configuration via HOME, not --profile, so
// "$HOME/.config/qBittorrent/qBittorrent.conf" is correct there.
func TestAlpineConfWrittenWhereProfileReads(t *testing.T) {
	files := torrentAlpineWriteFiles("/share0", nil)

	var confPath string
	for _, f := range files {
		if strings.HasSuffix(f.Path, "qBittorrent.conf") {
			confPath = f.Path
		}
	}
	if confPath == "" {
		t.Fatal("no qBittorrent.conf is written")
	}

	want := torrentAlpineConfigDir + "/qBittorrent/config/qBittorrent.conf"
	if confPath != want {
		t.Errorf("config written to %q, but --profile=%s makes qBittorrent read %q",
			confPath, torrentAlpineConfigDir, want)
	}

	// The OpenRC service must keep naming the profile root this path derives
	// from; if the two drift apart the config goes unread again.
	if !strings.Contains(torrentQbittorrentOpenRCService(),
		"--profile="+torrentAlpineConfigDir) {
		t.Errorf("the OpenRC service must pass --profile=%s", torrentAlpineConfigDir)
	}
}

// TestAlpineCreatesConfDir guards the directory the config lands in. cloud-init
// writes the .conf and a runcmd chowns the tree to the guest user; if the
// runcmd creates only the parent, the chown -R misses nothing but the mkdir no
// longer describes where the file actually goes.
func TestAlpineCreatesConfDir(t *testing.T) {
	joined := strings.Join(torrentAlpineRunCmds(nil, nil, nil), "\n")

	if !strings.Contains(joined, "mkdir -p "+torrentAlpineConfigDir+"/qBittorrent/config") {
		t.Error("the runcmds must create the qBittorrent/config directory the profile resolves to")
	}
}

// TestAlpineBindsQbittorrentToWG0 covers the Alpine base's binding. It has one
// tunnel interface for every provider: a NordVPN token is exchanged for a
// NordLynx WireGuard config before the write files are built, so there is no
// "nordlynx" interface here the way there is on the Ubuntu base. Binding to
// that name would name an interface that never appears.
func TestAlpineBindsQbittorrentToWG0(t *testing.T) {
	wgConf := &vpn.WireGuardConfig{
		PrivateKey: "k", PublicKey: "p", Endpoint: "192.0.2.10:51820",
	}

	var conf string
	for _, f := range torrentAlpineWriteFiles("/downloads", wgConf) {
		if f.Path == torrentAlpineConfPath {
			conf = f.Content
		}
	}
	if conf == "" {
		t.Fatal("no qBittorrent.conf is written")
	}

	for _, want := range []string{
		"Session\\Interface=wg0",
		"Session\\InterfaceName=wg0",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("the Alpine base must bind qBittorrent to wg0: missing %q\ngot:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "nordlynx") {
		t.Error("the Alpine base runs no nordlynx interface: NordLynx arrives as a wg0 config")
	}
}

// TestAlpineUnboundWithoutVPN pins the no-VPN case: nothing to bind to, and
// binding to an interface that never appears would stop the session talking.
func TestAlpineUnboundWithoutVPN(t *testing.T) {
	var conf string
	for _, f := range torrentAlpineWriteFiles("/downloads", nil) {
		if f.Path == torrentAlpineConfPath {
			conf = f.Content
		}
	}
	if conf == "" {
		t.Fatal("no qBittorrent.conf is written")
	}
	if strings.Contains(conf, "Session\\InterfaceName=") {
		t.Errorf("no VPN means no interface to bind to\ngot:\n%s", conf)
	}
}
