package templates

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Benehiko/vee/internal/cloudinit"
	"github.com/Benehiko/vee/internal/vpn"
)

// TestQbittorrentConfTempPath covers the split between the save path and the
// in-progress path. Callers point TempPath at local disk so that the random
// small writes of an in-progress torrent never cross a network filesystem;
// only the completed file is moved to the save path.
func TestQbittorrentConfTempPath(t *testing.T) {
	conf := qbittorrentConf("/downloads/movies", incompletePath, "wg0")

	for _, want := range []string{
		"Session\\DefaultSavePath=/downloads/movies",
		"Session\\TempPath=" + incompletePath,
		"Session\\TempPathEnabled=true",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("qbittorrentConf() missing %q\ngot:\n%s", want, conf)
		}
	}

	// The old behaviour nested incomplete/ under the save path, which put
	// in-progress writes on the share.
	if strings.Contains(conf, "Session\\TempPath=/downloads/movies/incomplete") {
		t.Error("TempPath must not nest under the save path: in-progress writes would land on the share")
	}
}

// TestQbittorrentConfWebUIThroughTunnel covers WebUI access via vee tunnel.
// The tunnel proxies through a random local port, so the browser's Host
// header ("localhost:42903") never matches the WebUI port — qBittorrent's
// host header validation 401s every such request.
func TestQbittorrentConfWebUIThroughTunnel(t *testing.T) {
	conf := qbittorrentConf("/downloads", "", "wg0")
	if !strings.Contains(conf, "WebUI\\HostHeaderValidation=false") {
		t.Errorf("host header validation must be off, or tunnelled requests get 401:\n%s", conf)
	}
}

// TestQbittorrentConfTempPathFallback checks that an empty tempPath keeps the
// pre-split behaviour rather than producing a bare "incomplete" relative path.
func TestQbittorrentConfTempPathFallback(t *testing.T) {
	conf := qbittorrentConf("/downloads", "", "wg0")
	if !strings.Contains(conf, "Session\\TempPath=/downloads/incomplete") {
		t.Errorf("empty tempPath should fall back to <savePath>/incomplete\ngot:\n%s", conf)
	}
}

// TestNFSServers covers de-duplication: several exports usually live on one
// server, and each server needs exactly one kill-switch exception.
func TestNFSServers(t *testing.T) {
	cases := []struct {
		name   string
		mounts []NFSMount
		want   []string
	}{
		{
			name:   "empty",
			mounts: nil,
			want:   nil,
		},
		{
			name: "duplicates collapsed, order preserved",
			mounts: []NFSMount{
				{Server: "192.168.178.76", Export: "/mnt/Data/Movies"},
				{Server: "192.168.178.76", Export: "/mnt/Data/Shows"},
				{Server: "10.0.0.5", Export: "/export"},
			},
			want: []string{"192.168.178.76", "10.0.0.5"},
		},
		{
			name:   "empty server skipped",
			mounts: []NFSMount{{Export: "/mnt/Data/Movies"}},
			want:   nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nfsServers(tc.mounts)
			if len(got) != len(tc.want) {
				t.Fatalf("nfsServers() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("nfsServers()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestNFSBypassRules covers the unconditional VPN bypass: NFS servers sit on
// the LAN, not behind the tunnel, so every mount needs an outbound hole
// regardless of whether a VPN is configured.
func TestNFSBypassRules(t *testing.T) {
	mounts := []NFSMount{
		{Server: "192.168.178.76", Export: "/mnt/Data/Movies", GuestPath: "/downloads/movies"},
		{Server: "192.168.178.76", Export: "/mnt/Data/Shows", GuestPath: "/downloads/shows"},
	}

	got := nfsBypassRules(mounts)
	if len(got) != 1 {
		t.Fatalf("two exports on one server should yield one rule, got %v", got)
	}
	if got[0] != "ufw allow out to 192.168.178.76" {
		t.Errorf("nfsBypassRules() = %q", got[0])
	}

	if rules := nfsBypassRules(nil); len(rules) != 0 {
		t.Errorf("no mounts should yield no rules, got %v", rules)
	}
}

// testWGConf returns a WireGuard config with a hostname endpoint — the shape
// that exposes the resolve-before-deny requirement, since a hostname cannot be
// looked up once the deny policy is active.
func testWGConf() *vpn.WireGuardConfig {
	return &vpn.WireGuardConfig{
		PrivateKey: "cHJpdmF0ZQ==",
		PublicKey:  "cHVibGlj",
		Address:    "10.5.0.2/32",
		DNS:        "10.5.0.1",
		Endpoint:   "vpn.example.com:51820",
	}
}

// TestWireGuardBypassOrdering pins rule ordering against the kill-switch. The
// NFS hole must exist before wg-quick brings the tunnel up, or the mount races
// the kill-switch.
//
// Note this asserts nothing about the hole's position relative to "ufw default
// deny outgoing": ufw default policies are chain policies applied at the end of
// evaluation, not ordered rules, so either order yields the same ruleset.
func TestWireGuardBypassOrdering(t *testing.T) {
	wgCmds := torrentWGKillSwitchCmds(testWGConf(), []NFSMount{{Server: "192.168.178.76"}})

	idx := func(want string) int {
		for i, c := range wgCmds {
			if c == want {
				return i
			}
		}
		t.Fatalf("command %q not found in %v", want, wgCmds)
		return -1
	}

	allow := idx("ufw allow out to 192.168.178.76")
	tunnel := idx("systemctl enable --now wg-quick@wg0")

	if allow > tunnel {
		t.Error("NFS allow must come before wg-quick, or the mount races the kill-switch")
	}
}

// TestWireGuardHandshakeHoleIsPinned is the security-critical test for the
// kill-switch. Two things have to hold at once: the handshake must be able to
// leave (or the tunnel can never come up from behind the deny policy), and the
// hole that lets it leave must be pinned to the endpoint's own addresses.
//
// An unpinned "allow out 51820/udp" would satisfy the first and betray the
// second: any process could then reach any host listening on that port with the
// tunnel down, which is a usable covert channel.
func TestWireGuardHandshakeHoleIsPinned(t *testing.T) {
	cmds := torrentWGKillSwitchCmds(testWGConf(), nil)
	joined := strings.Join(cmds, "\n")

	if !strings.Contains(joined, "getent ahostsv4") {
		t.Error("the endpoint must be resolved before the deny policy; there is no DNS left afterwards")
	}
	if !strings.Contains(joined, wgEndpointAddrsFile) {
		t.Errorf("resolved addresses must be recorded in %s so later boots can reuse them", wgEndpointAddrsFile)
	}
	if !strings.Contains(joined, "port 51820 proto udp") {
		t.Error("no handshake hole: the tunnel can never be established from behind the kill-switch")
	}

	// The hole must name the resolved addresses, never the port alone.
	for _, c := range cmds {
		if !strings.Contains(c, "proto udp") {
			continue
		}
		if !strings.Contains(c, wgEndpointAddrsFile) {
			t.Errorf("handshake hole is not pinned to the endpoint addresses: %q", c)
		}
	}

	// Resolution has to precede the deny policy, or the lookup it depends on is
	// itself blocked.
	resolve, deny := -1, -1
	for i, c := range cmds {
		switch {
		// The build-time resolve, not the endpoint-refresh script: that
		// script embeds the same command text but is installed after the
		// deny policy on purpose, since it runs later with a DNS hole of
		// its own.
		case strings.Contains(c, "getent ahostsv4") && !strings.Contains(c, wgRefreshScriptPath):
			resolve = i
		case c == "ufw default deny outgoing":
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

// TestWireGuardTunnelRetryPersists covers the reboot path. wg-quick@wg0 is
// enabled so systemd starts it every boot, but the upstream unit sets no
// Restart= and ufw restores the deny policy earlier in boot — so a handshake
// that fails at that moment is never retried and the guest sits firewalled with
// no tunnel until someone opens a console.
func TestWireGuardTunnelRetryPersists(t *testing.T) {
	joined := strings.Join(torrentWGKillSwitchCmds(testWGConf(), nil), "\n")

	if !strings.Contains(joined, "vee-wg-retry.timer") {
		t.Error("no retry timer: a tunnel that fails at boot never comes back")
	}
	if !strings.Contains(joined, "systemctl enable --now vee-wg-retry.timer") {
		t.Error("the retry timer must be enabled, or it does not survive the reboot it exists for")
	}
	// The recovery must use restart, not start. wg-quick@ is Type=oneshot with
	// RemainAfterExit=yes, so once its ExecStart has succeeded systemd reports
	// the unit active-exited even after the interface is gone — and "start"
	// against an active unit is a no-op that exits 0. Verified live: with wg0
	// torn down, the timer fired every 60s and reported success while the
	// tunnel stayed down indefinitely.
	if !strings.Contains(joined, "systemctl restart wg-quick@wg0") {
		t.Error("the retry must restart wg-quick@wg0: start is a no-op while the unit is still active-exited")
	}
}

// TestTorrentRuncmdOrdering pins the ordering constraint a real first boot
// exposed: starting qBittorrent before the shares are mounted sends downloads
// to the VM's own disk instead of the NAS.
func TestTorrentRuncmdOrdering(t *testing.T) {
	cmds := append(torrentBaseRunCmds(), torrentMountAndAppCmds(
		nil,
		[]NFSMount{{Server: "192.168.178.76", Export: "/mnt/Data/Movies", GuestPath: "/downloads/movies"}},
	)...)
	idx := func(substr string) int {
		for i, c := range cmds {
			if strings.Contains(c, substr) {
				return i
			}
		}
		t.Fatalf("no runcmd containing %q in:\n%s", substr, strings.Join(cmds, "\n"))
		return -1
	}

	mount := idx("mount -t nfs4")
	chownIncomplete := idx("chown -R vee " + incompletePath)
	start := idx("systemctl enable --now qbittorrent-nox@vee")
	ufwEnable := idx("ufw --force enable")

	if mount < ufwEnable {
		t.Error("NFS mount must follow the ufw rules, not precede them")
	}
	if start < mount {
		t.Error("qBittorrent must start after the shares are mounted, or downloads land on the local disk")
	}
	if chownIncomplete > start {
		t.Error("the incomplete dir must be chowned before qBittorrent starts")
	}
	// Every chown has to come after the users module, which cloud-init runs
	// before runcmd — so what matters here is that no chown is prepended ahead
	// of the base commands.
	for i, c := range cmds {
		if strings.HasPrefix(c, "chown") && i < ufwEnable {
			t.Errorf("chown at index %d precedes the base runcmds: %q", i, c)
		}
	}
}

// TestChownNeverNamesAGroup pins the fix for a real boot failure. The vee
// account is rendered with no_user_group (see internal/cloudinit), so there is
// no "vee" group: any "chown vee:vee" fails with "chown: invalid group", and
// the same owner on a deferred write_files entry fails cloud-final.service.
func TestChownNeverNamesAGroup(t *testing.T) {
	cmds := torrentMountAndAppCmds(
		[]ShareMount{{HostDir: "/srv/media", GuestPath: "/downloads/virtiofs"}},
		[]NFSMount{{Server: "192.168.178.76", Export: "/mnt/Data/Movies", GuestPath: "/downloads/movies"}},
	)

	for _, c := range cmds {
		if strings.Contains(c, "vee:vee") {
			t.Errorf("runcmd names a nonexistent vee group: %q", c)
		}
	}

	// And the chowns must still be there — dropping them entirely would leave
	// the incomplete dir owned by root and unwritable by qbittorrent-nox.
	var chowns int
	for _, c := range cmds {
		if strings.HasPrefix(c, "chown ") || strings.HasPrefix(c, "chown -R ") {
			chowns++
		}
	}
	if chowns == 0 {
		t.Error("no chown commands generated; the incomplete dir would stay root-owned")
	}
}

// TestNordVPNCmdOrdering pins a sequence that hangs cloud-init when it is
// wrong. Verified by hand on a live guest: the snap interfaces must be
// connected before any nordvpn command works, and "set analytics off" must
// precede the login or the login blocks forever on an interactive consent
// prompt that cloud-init cannot answer.
func TestNordVPNCmdOrdering(t *testing.T) {
	cmds := nordVPNCmds("tok", `nordvpn connect "sweden"`,
		[]NFSMount{{Server: "192.168.178.76", Export: "/mnt/Data/Movies", GuestPath: "/downloads/movies"}})

	idx := func(substr string) int {
		for i, c := range cmds {
			if strings.Contains(c, substr) {
				return i
			}
		}
		t.Fatalf("no command containing %q in:\n%s", substr, strings.Join(cmds, "\n"))
		return -1
	}

	install := idx("snap install nordvpn")
	connectIface := idx("snap connect nordvpn:network-control")
	analytics := idx("set analytics off")
	login := idx("nordvpn login --token")
	whitelist := idx("whitelist add subnet 192.168.178.76/32")
	connect := idx("nordvpn connect")

	if connectIface < install {
		t.Error("interfaces must be connected after the snap is installed")
	}
	if analytics < connectIface {
		t.Error("set analytics needs the snap interfaces connected first")
	}
	if login < analytics {
		t.Error("login before 'set analytics off' blocks forever on the consent prompt")
	}
	if whitelist > connect {
		t.Error("the NFS whitelist must be registered before connecting, or mounts race the kill-switch")
	}
	if connect != len(cmds)-1 {
		t.Errorf("connect must be last, got index %d of %d", connect, len(cmds))
	}
}

// TestNordVPNGroupAccess covers unprivileged daemon access. The daemon only
// answers root or members of the "nordvpn" group, and the snap does not create
// the group — without these commands "nordvpn status" as the vee user prints
// "We couldn't reach System Daemon", which reads like the VPN is down when it
// is actually connected.
func TestNordVPNGroupAccess(t *testing.T) {
	cmds := nordVPNCmds("tok", "nordvpn connect", nil)
	joined := strings.Join(cmds, "\n")

	if !strings.Contains(joined, "groupadd -f nordvpn") {
		t.Errorf("nordvpn group is never created:\n%s", joined)
	}
	if !strings.Contains(joined, "usermod -aG nordvpn vee") {
		t.Errorf("vee is not added to the nordvpn group; unprivileged status checks would fail:\n%s", joined)
	}
	if !strings.Contains(joined, "usermod -aG nordvpn ubuntu") {
		t.Errorf("ubuntu is not added to the nordvpn group; `vee network` probes as the cloud-init default user and its nordvpn checks would fail:\n%s", joined)
	}
}

// TestNordVPNWhitelistsSSHOnly covers management access through the
// kill-switch. With it up the guest stops answering ARP on the LAN, so port 22
// is the only way in short of the console — but only port 22: vee tunnel
// forwards services over the SSH connection (ssh -L), so exposing the
// qBittorrent port to the LAN would be a hole with no purpose.
func TestNordVPNWhitelistsSSHOnly(t *testing.T) {
	cmds := nordVPNCmds("tok", "nordvpn connect", nil)
	joined := strings.Join(cmds, "\n")

	if !strings.Contains(joined, "nordvpn whitelist add port 22") {
		t.Errorf("SSH is not whitelisted; the guest would be console-only:\n%s", joined)
	}
	if strings.Contains(joined, "port 8080") {
		t.Error("qBittorrent's port must stay closed: vee tunnel forwards it over SSH")
	}

	// The whitelist has to be registered before the connection comes up.
	var whitelist, connect int
	for i, c := range cmds {
		switch {
		case strings.Contains(c, "whitelist add port 22"):
			whitelist = i
		case strings.HasPrefix(c, "nordvpn connect"):
			connect = i
		}
	}
	if whitelist > connect {
		t.Error("the SSH whitelist must precede connect, or the kill-switch locks the guest first")
	}
}

// TestWireGuardAllowsSSHOutbound covers the same management path for the
// WireGuard kill-switch, which works through ufw rather than a daemon. The
// inbound "ufw allow OpenSSH" in the base rules is not sufficient: the
// outbound half of an established SSH connection leaves on the LAN interface,
// not wg0, so "default deny outgoing" would drop the replies.
func TestWireGuardAllowsSSHOutbound(t *testing.T) {
	wgCmds := torrentWGKillSwitchCmds(testWGConf(), nil)

	allowSSH, tunnel := -1, -1
	for i, c := range wgCmds {
		switch c {
		case "ufw allow out 22/tcp":
			allowSSH = i
		case "systemctl enable --now wg-quick@wg0":
			tunnel = i
		}
	}
	if allowSSH < 0 {
		t.Fatal("no outbound SSH hole: the guest becomes unreachable once the policy is active")
	}
	if tunnel < 0 {
		t.Fatal("the tunnel is never brought up")
	}
	if allowSSH > tunnel {
		t.Error("the SSH hole must exist before the tunnel comes up")
	}
}

// TestAppendFstab checks the generated runcmd is idempotent: cloud-init may be
// re-run, and a duplicated fstab entry makes the guest fail to boot cleanly.
func TestAppendFstab(t *testing.T) {
	line := fstabEntry("192.168.178.76:/mnt/Data/Movies", "/downloads/movies", "nfs4", nfsMountOptions)
	cmd := appendFstab(line, "/downloads/movies")

	if !strings.HasPrefix(cmd, "grep -qs ") {
		t.Errorf("appendFstab must guard against duplicates, got %q", cmd)
	}
	if !strings.Contains(cmd, "/etc/fstab") {
		t.Errorf("appendFstab targets /etc/fstab, got %q", cmd)
	}
	// The target is matched space-delimited so /downloads does not match
	// /downloads/movies.
	if !strings.Contains(cmd, "' /downloads/movies '") {
		t.Errorf("fstab guard must match the target space-delimited, got %q", cmd)
	}
}

// TestFstabEntryNFSOptions pins the NFS defaults. hard (not soft) is the
// important one: a soft mount surfaces a NAS hiccup as EIO mid-write, which
// qBittorrent reports as an errored torrent instead of retrying.
func TestFstabEntryNFSOptions(t *testing.T) {
	line := fstabEntry("192.168.178.76:/mnt/Data/Movies", "/downloads/movies", "nfs4", nfsMountOptions)

	want := "192.168.178.76:/mnt/Data/Movies /downloads/movies nfs4 rw,hard,proto=tcp,timeo=600,retrans=2,_netdev 0 0"
	if line != want {
		t.Errorf("fstabEntry() = %q, want %q", line, want)
	}
	if strings.Contains(line, "soft") {
		t.Error("NFS mounts must be hard: soft returns EIO mid-write and errors the torrent")
	}
}

// TestTorrentUserDataIsValidYAML renders the template's cloud-init exactly as
// vm.Manager does and parses it.
//
// This is a regression test for a failure that is silent and total. cloud-init
// renders each single-line runcmd as a bare YAML scalar, so a command starting
// with "[" — the ordinary shell `[ -f foo ]` test — parses as a flow sequence
// and invalidates the whole user-data document. cloud-init then discards every
// module: the guest boots with no packages, no SSH keys, no services, and the
// only evidence is one "Failed loading yaml blob" line in the serial log.
//
// The kill-switch commands are the ones at risk here: they embed shell loops,
// quoted variables and printf'd unit files with newlines in them.
func TestTorrentUserDataIsValidYAML(t *testing.T) {
	nfsMounts := []NFSMount{{
		Server:    "192.168.178.76",
		Export:    "/mnt/Data/Movies",
		GuestPath: "/downloads/movies",
	}}

	runCmds := torrentWGKillSwitchCmds(testWGConf(), nfsMounts)
	runCmds = append(runCmds, torrentBaseRunCmds()...)
	runCmds = append(runCmds, torrentMountAndAppCmds(nil, nfsMounts)...)

	rendered, err := cloudinit.RenderUserData(&cloudinit.Config{
		Hostname:    "dl",
		User:        "vee",
		DefaultUser: "ubuntu",
		SSHKeys:     []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAtest"},
		RunCmds:     runCmds,
		WriteFiles: []cloudinit.WriteFile{
			{
				Path:        "/etc/wireguard/wg0.conf",
				Content:     vpn.RenderWireGuardConf(testWGConf()),
				Permissions: "0600",
			},
		},
	})
	if err != nil {
		t.Fatalf("render user-data: %v", err)
	}

	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(rendered), &parsed); err != nil {
		t.Fatalf("rendered user-data is not valid YAML (cloud-init would discard every module): %v", err)
	}

	// Every runcmd must survive as a string. A command parsed as a list is the
	// exact symptom of the bare-"[" bug, and it would reach the guest mangled
	// even in a document that happened to stay parseable.
	cmds, ok := parsed["runcmd"].([]any)
	if !ok {
		t.Fatal("rendered user-data has no runcmd list")
	}
	for i, c := range cmds {
		if _, ok := c.(string); !ok {
			t.Errorf("runcmd[%d] parsed as %T, not a string: %v", i, c, c)
		}
	}
}

// TestWireGuardDoesNotInstallResolvconf guards a package that must never come
// back. wg-quick honours the "DNS =" directive by shelling out to a
// resolvconf(8) command, and on Ubuntu 24.04 systemd-resolved is what supplies
// it: the package declares "Provides: resolvconf" and ships /usr/sbin/resolvconf
// as a symlink to resolvectl. It has Priority: important and is active on the
// cloud image out of the box.
//
// Naming resolvconf explicitly did not add anything. Both the standalone
// resolvconf and openresolv packages were dropped in noble, so apt found no
// installation candidate and cloud-init settled on "status: error" for every
// WireGuard torrent VM — harmless to the running services, since cloud-init
// retries the rest of the list individually, but it permanently poisons the
// exit status that genuine failures would otherwise show up in.
//
// Had it been installable it would have been worse than useless: wg-quick only
// prepends its "tun." interface prefix when /etc/resolvconf/interface-order
// exists, a file that package ships, and resolvectl's compat shim rejects the
// prefixed name ("Failed to resolve interface"). Installing it would have
// broken the DNS it was meant to enable.
//
// NewTorrentConfig downloads a disk image, so the package list cannot be built
// in a unit test; assert against the source that produces it instead.
func TestWireGuardDoesNotInstallResolvconf(t *testing.T) {
	src, err := os.ReadFile("torrent.go")
	if err != nil {
		t.Fatalf("read torrent.go: %v", err)
	}

	for _, line := range strings.Split(string(src), "\n") {
		code, _, _ := strings.Cut(line, "//")
		if !strings.Contains(code, "packages = append(") {
			continue
		}
		if strings.Contains(code, `"resolvconf"`) || strings.Contains(code, `"openresolv"`) {
			t.Errorf("resolvconf has no installation candidate on Ubuntu 24.04; "+
				"installing it poisons cloud-init's exit status and breaks wg-quick's "+
				"DNS handling. systemd-resolved already provides resolvconf(8): %q",
				strings.TrimSpace(line))
		}
	}
}

// TestWebUINeverExposedOnUbuntu pins the Ubuntu base to the same access model
// the Alpine base and the NordVPN path already enforce: SSH is the only way in,
// and vee tunnel forwards the web UI over it, so 8080 never gets a hole.
//
// torrentBaseRunCmds used to carry "ufw allow 8080/tcp", which put the listener
// on the LAN for every --nic-mode=bridge VM while the Alpine template's own
// comment claimed the UI "never needs a hole of its own". The rule bought
// nothing even for someone who wanted LAN access: no WebUI\Password is ever
// set, and qBittorrent is configured with LocalHostAuth=false, so it skips
// authentication for loopback only and answers a LAN client with 403.
//
// The tunnel is unaffected. HostFwds binds 127.0.0.1 on the host and the
// request reaches qBittorrent over guest loopback, which is both what the ufw
// rule never governed and what the auth bypass keys on.
func TestWebUINeverExposedOnUbuntu(t *testing.T) {
	joined := strings.Join(torrentBaseRunCmds(), "\n")

	if strings.Contains(joined, "8080") {
		t.Error("qBittorrent's port must stay closed: vee tunnel forwards it over SSH")
	}
	if !strings.Contains(joined, "ufw allow OpenSSH") {
		t.Error("SSH is the only way into the guest and must be allowed inbound")
	}
}

// TestWebUIStaysTunnellable guards the other half of the change. Closing the
// firewall hole must not disturb the pieces vee tunnel actually depends on: the
// service entry it reads the port from, and the host-loopback forward the
// request arrives on. Dropping either would make the UI unreachable rather than
// merely LAN-closed.
func TestWebUIStaysTunnellable(t *testing.T) {
	src, err := os.ReadFile("torrent.go")
	if err != nil {
		t.Fatalf("read torrent.go: %v", err)
	}
	s := string(src)

	if !strings.Contains(s, `{Name: "qbittorrent", Port: 8080, Protocol: vm.ServiceHTTP}`) {
		t.Error("the qbittorrent service entry is what vee tunnel resolves the port from")
	}
	if !strings.Contains(s, `"tcp:127.0.0.1:8080-:8080"`) {
		t.Error("the host forward must stay bound to 127.0.0.1: it is the tunnel's path in, " +
			"and guest loopback is what qBittorrent's LocalHostAuth bypass keys on")
	}
}

// TestWGEndpointRefreshOnlyForHostnames pins the refresh machinery to the case
// that needs it. An IP-literal endpoint cannot be re-addressed, so installing a
// script, rewriting the retry unit and re-resolving on every tick would be pure
// overhead — and overhead that touches the firewall.
func TestWGEndpointRefreshOnlyForHostnames(t *testing.T) {
	literal := &vpn.WireGuardConfig{
		PrivateKey: "k", PublicKey: "p", Endpoint: "192.0.2.10:51820",
	}
	if got := torrentWGEndpointRefreshCmds(literal); got != nil {
		t.Errorf("a literal-IP endpoint needs no refresh machinery, got %v", got)
	}
	if wf := wgRefreshWriteFile(literal, ufwFirewallCmds(literal)); wf != nil {
		t.Errorf("a literal-IP endpoint needs no refresh script, got %+v", wf)
	}

	// IPv6 literals are literals too.
	v6 := &vpn.WireGuardConfig{
		PrivateKey: "k", PublicKey: "p", Endpoint: "[2001:db8::1]:51820",
	}
	if got := torrentWGEndpointRefreshCmds(v6); got != nil {
		t.Errorf("a literal IPv6 endpoint needs no refresh machinery, got %v", got)
	}

	if got := torrentWGEndpointRefreshCmds(testWGConf()); got == nil {
		t.Error("a hostname endpoint must get the refresh machinery: the pinned hole " +
			"would otherwise name the old address forever after the provider re-addresses")
	}
	if wf := wgRefreshWriteFile(testWGConf(), ufwFirewallCmds(testWGConf())); wf == nil {
		t.Error("a hostname endpoint must get the refresh script")
	}
}

// TestWGRefreshScriptFailsClosed pins the properties that make the refresh safe
// to run unattended from a timer. Each one, if broken, converts a silent outage
// into either a leak or a worse outage.
func TestWGRefreshScriptFailsClosed(t *testing.T) {
	cfg := testWGConf()
	script := wgRefreshEndpointScript(cfg, ufwFirewallCmds(cfg))

	// An empty answer must leave the existing pins alone. Clearing them would
	// strand a guest whose tunnel was working, and rewriting them from an empty
	// result would drop the handshake hole entirely.
	if !strings.Contains(script, `if [ -z "$NEW" ]`) {
		t.Error("no guard on an empty resolve result: a failed lookup must keep the old pins")
	}

	// The DNS hole must be withdrawn on every path, including the one where the
	// lookup failed — otherwise a resolver outage leaves port 53 open to the
	// nameservers indefinitely.
	allowIdx := strings.Index(script, "ufw allow out to \"$ns\" port 53")
	denyIdx := strings.Index(script, "ufw delete allow out to \"$ns\" port 53")
	guardIdx := strings.Index(script, `if [ -z "$NEW" ]`)
	switch {
	case allowIdx < 0 || denyIdx < 0:
		t.Fatal("the lookup needs a DNS hole and must close it again")
	case denyIdx < allowIdx:
		t.Error("the DNS hole is closed before it is opened")
	case denyIdx > guardIdx:
		t.Error("the DNS hole is closed only after the empty-result guard: a failed " +
			"lookup would leave port 53 open")
	}

	// The DNS hole is pinned to specific nameservers, never opened wholesale.
	if strings.Contains(script, "ufw allow out 53") {
		t.Error("an unpinned DNS hole is a covert channel; pin it to the nameservers")
	}

	// New holes go in before old ones come out, so the handshake is never
	// without a way through.
	newIdx := strings.Index(script, "for ip in $NEW; do")
	oldIdx := strings.Index(script, "for ip in $OLD; do")
	if newIdx < 0 || oldIdx < 0 {
		t.Fatal("the script must both install new holes and withdraw superseded ones")
	}
	if oldIdx < newIdx {
		t.Error("old holes are withdrawn before the new ones are installed, leaving a gap")
	}

	// A corrected firewall alone is not enough: wg-quick freezes the peer
	// address at "up" time, so the tunnel has to re-read its config.
	if !strings.Contains(script, "systemctl restart wg-quick@wg0") {
		t.Error("nothing forces wg-quick to re-read the endpoint; the firewall would be " +
			"correct while the tunnel kept dialling the old address")
	}
}

// TestWGRefreshRunsBeforeTunnelRecovery covers the ordering inside the retry
// unit. The refresh has to run first: restarting the tunnel against a hole that
// still names the old address just fails again.
func TestWGRefreshRunsBeforeTunnelRecovery(t *testing.T) {
	joined := strings.Join(torrentWGEndpointRefreshCmds(testWGConf()), "\n")

	refresh := strings.Index(joined, wgRefreshScriptPath)
	recover := strings.Index(joined, "wg show wg0")
	if refresh < 0 || recover < 0 {
		t.Fatalf("the retry unit must both refresh the endpoint and recover the tunnel:\n%s", joined)
	}
	if refresh > recover {
		t.Error("the tunnel is restarted before the endpoint is re-resolved; the restart " +
			"would race a firewall hole that still names the old address")
	}
}

// TestQbittorrentConfBindsToTunnel covers the interface binding, the second
// layer under the kill-switch. The firewall is a policy and the binding is an
// address selection, so they fail differently: qBittorrent can start before the
// tunnel is up, and on the Ubuntu base the NordVPN daemon enforces its
// kill-switch itself rather than through ufw, so a dead daemon takes the policy
// with it. In both cases an unbound session announces the guest's LAN address.
func TestQbittorrentConfBindsToTunnel(t *testing.T) {
	for _, iface := range []string{"wg0", "nordlynx"} {
		conf := qbittorrentConf("/downloads", incompletePath, iface)

		for _, want := range []string{
			"Session\\Interface=" + iface,
			"Session\\InterfaceName=" + iface,
		} {
			if !strings.Contains(conf, want) {
				t.Errorf("qbittorrentConf(%q) missing %q\ngot:\n%s", iface, want, conf)
			}
		}

		// Never an address. The tunnel address is assigned at connect time and
		// rotates on reconnect; libtorrent resolves the name to the current
		// address itself, which a baked-in address could not follow.
		if strings.Contains(conf, "Session\\InterfaceAddress=") {
			t.Errorf("binding must be by name only: an address cannot follow a reconnect\ngot:\n%s", conf)
		}
	}
}

// TestQbittorrentConfUnboundWithoutVPN pins the no-VPN case. There is no tunnel
// interface to bind to, and binding to one that never appears would stop the
// session talking at all — the guest routes over the LAN there by design.
func TestQbittorrentConfUnboundWithoutVPN(t *testing.T) {
	conf := qbittorrentConf("/downloads", incompletePath, "")

	for _, unwanted := range []string{
		"Session\\Interface=",
		"Session\\InterfaceName=",
	} {
		if strings.Contains(conf, unwanted) {
			t.Errorf("no VPN means no interface to bind to, but found %q\ngot:\n%s", unwanted, conf)
		}
	}
}

// TestQbittorrentConfMatchesKillSwitchOnIPv6 keeps the session and the firewall
// saying the same thing. The kill-switch drops IPv6 outright, so every v6 peer
// and announce the session attempts is a connection that cannot complete —
// wasted connection slots and announce timeouts rather than a leak.
func TestQbittorrentConfMatchesKillSwitchOnIPv6(t *testing.T) {
	conf := qbittorrentConf("/downloads", incompletePath, "wg0")
	if !strings.Contains(conf, "Session\\IPv6Enabled=false") {
		t.Errorf("IPv6 must be off to match the kill-switch\ngot:\n%s", conf)
	}
}

// TestQbittorrentConfFixedListenPort covers reproducibility across reboots.
// qBittorrent randomises the listen port on every start unless one is pinned,
// which also defeats any port forward a VPN provider hands out.
func TestQbittorrentConfFixedListenPort(t *testing.T) {
	conf := qbittorrentConf("/downloads", incompletePath, "wg0")

	for _, want := range []string{
		"Session\\Port=" + strconv.Itoa(qbittorrentListenPort),
		"Session\\UseRandomPort=false",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("qbittorrentConf() missing %q\ngot:\n%s", want, conf)
		}
	}
}

// TestQbittorrentConfWebUIBindsLoopback pins the WebUI to guest loopback.
//
// A wildcard bind gained nothing: LocalHostAuth=false skips authentication for
// loopback only, so a LAN client is answered 403, and no WebUI\Password is ever
// set. But in bridge mode the guest holds a real LAN address, where the only
// thing keeping 8080 unreachable is the kill-switch. Binding narrowly means the
// firewall is not the sole thing standing between the LAN and an
// authentication-bypassing listener.
//
// The tunnel is unaffected: HostFwds binds 127.0.0.1 on the host and the
// request reaches qBittorrent over guest loopback, which is exactly what both
// this bind and the LocalHostAuth bypass key on.
func TestQbittorrentConfWebUIBindsLoopback(t *testing.T) {
	conf := qbittorrentConf("/downloads", incompletePath, "wg0")

	if !strings.Contains(conf, "WebUI\\Address=127.0.0.1") {
		t.Errorf("the WebUI must bind guest loopback, not the LAN\ngot:\n%s", conf)
	}
	if strings.Contains(conf, "WebUI\\Address=*") {
		t.Error("a wildcard bind puts an auth-bypassing listener on the LAN in bridge mode")
	}
	// The tunnel's half of it must survive the narrowing.
	if !strings.Contains(conf, "WebUI\\LocalHostAuth=false") {
		t.Error("the loopback auth bypass is what makes the tunnelled UI usable")
	}
}

// TestQbittorrentConfAnonymousModeOn pins anonymous mode together with the peer
// discovery it once excluded. On qBittorrent 2.9.0-3.2.5 the two were mutually
// exclusive: anonymous mode disabled DHT, LSD and UPnP/NAT-PMP outright. 3.3.0
// moved that to the separate "disable connections not supported by proxies"
// option, and every base installs 4.x or newer, so both must now hold at once.
// If a future base ever pins an ancient qBittorrent, this test still passes
// while the guest silently loses discovery — the version floor is the real
// guard, and this pins the intent.
func TestQbittorrentConfAnonymousModeOn(t *testing.T) {
	conf := qbittorrentConf("/downloads", incompletePath, "wg0")

	for _, want := range []string{
		"Session\\AnonymousModeEnabled=true",
		"Session\\DHTEnabled=true",
		"Session\\PeXEnabled=true",
		"Session\\LSDEnabled=true",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("anonymous mode and peer discovery must both stay on: missing %q\ngot:\n%s", want, conf)
		}
	}
}

// TestTorrentUbuntuBindsPerVPNBranch checks the wiring rather than the renderer:
// the Ubuntu base picks the interface name from whichever VPN branch it takes.
// The NordVPN client brings up "nordlynx"; a WireGuard config brings up "wg0".
// Getting this backwards binds the session to an interface that never appears,
// which stops it talking entirely.
func TestTorrentUbuntuBindsPerVPNBranch(t *testing.T) {
	src, err := os.ReadFile("torrent.go")
	if err != nil {
		t.Fatalf("read torrent.go: %v", err)
	}
	s := string(src)

	if !strings.Contains(s, `bindIface = "nordlynx"`) {
		t.Error("the NordVPN branch must bind to nordlynx: that is the interface its client creates")
	}
	if !strings.Contains(s, `bindIface = "wg0"`) {
		t.Error("the WireGuard branch must bind to wg0")
	}
	if !strings.Contains(s, "qbittorrentConf(savePath, incompletePath, bindIface)") {
		t.Error("the rendered config must receive the branch's interface name")
	}
}
