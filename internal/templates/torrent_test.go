package templates

import (
	"strings"
	"testing"
)

// TestQbittorrentConfTempPath covers the split between the save path and the
// in-progress path. Callers point TempPath at local disk so that the random
// small writes of an in-progress torrent never cross a network filesystem;
// only the completed file is moved to the save path.
func TestQbittorrentConfTempPath(t *testing.T) {
	conf := qbittorrentConf("/downloads/movies", incompletePath)

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

// TestQbittorrentConfTempPathFallback checks that an empty tempPath keeps the
// pre-split behaviour rather than producing a bare "incomplete" relative path.
func TestQbittorrentConfTempPathFallback(t *testing.T) {
	conf := qbittorrentConf("/downloads", "")
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

// TestWireGuardBypassOrdering pins rule ordering against the kill-switch. ufw
// evaluates rules in order, so an allow emitted before "default deny outgoing"
// is overridden by it — and the hole must exist before wg-quick brings the
// tunnel up.
func TestWireGuardBypassOrdering(t *testing.T) {
	wgCmds := []string{
		"ufw default deny outgoing",
		"ufw default deny forward",
		"ufw allow out on wg0",
		"ufw allow out on lo",
	}
	wgCmds = append(wgCmds, nfsBypassRules([]NFSMount{{Server: "192.168.178.76"}})...)
	wgCmds = append(wgCmds, "systemctl enable --now wg-quick@wg0")

	idx := func(want string) int {
		for i, c := range wgCmds {
			if c == want {
				return i
			}
		}
		t.Fatalf("command %q not found in %v", want, wgCmds)
		return -1
	}

	deny := idx("ufw default deny outgoing")
	allow := idx("ufw allow out to 192.168.178.76")
	tunnel := idx("systemctl enable --now wg-quick@wg0")

	if allow < deny {
		t.Error("NFS allow must come after the deny policy, or ufw overrides it")
	}
	if allow > tunnel {
		t.Error("NFS allow must come before wg-quick, or the mount races the kill-switch")
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
