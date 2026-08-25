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
		case strings.Contains(c, "getent ahostsv4"):
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

// TestAlpineRejectsNordVPN pins the one capability the Alpine base cannot
// offer. NordVPN installs as a snap and Alpine has no snapd, so silently
// ignoring it would leave the guest with no VPN at all — the exact failure the
// kill-switch exists to prevent.
func TestAlpineRejectsNordVPN(t *testing.T) {
	_, err := NewTorrentAlpineConfig(
		t.Context(), nil, "dl", nil, nil, nil,
		&vpn.NordVPNConfig{Token: "tok"}, nil, "nordvpn",
	)
	if err == nil {
		t.Fatal("NordVPN on the Alpine base must be rejected, not silently ignored")
	}
	if !strings.Contains(err.Error(), "snap") {
		t.Errorf("the error should explain why NordVPN is unavailable, got %q", err)
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
