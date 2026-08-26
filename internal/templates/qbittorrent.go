package templates

import (
	"strconv"
	"strings"
)

// Fixed listen port for incoming BitTorrent connections.
//
// qBittorrent randomises the port on every start unless one is pinned, which
// makes behaviour irreproducible across reboots and defeats any port forward a
// VPN provider hands out. NordLynx forwards no port, so nothing can reach this
// listener there and the value only buys reproducibility; on a WireGuard
// provider that does forward one, this is the port to forward.
const qbittorrentListenPort = 6881

// qbittorrentConf returns the content of qBittorrent.conf pre-configured for:
//   - Forced encryption (no unencrypted connections)
//   - Aggressive peer settings (max connections, high peer turnover)
//   - Unlimited bandwidth
//   - Seed ratio 3.0 before stopping
//   - 20 active downloads + 20 active uploads
//   - Save path set to savePath (e.g. /downloads)
//   - In-progress torrents written to tempPath, which callers point at local
//     disk so that random small writes never cross a network filesystem
//   - Every peer connection bound to the VPN interface named by iface
//
// iface is the guest's tunnel interface ("wg0", or "nordlynx" on the Ubuntu
// base's NordVPN path). Binding is a second, independent layer under the
// kill-switch rather than a replacement for it: the kill-switch is a firewall
// policy and the binding is an address selection, so they fail differently.
// Two cases the firewall alone does not cover:
//
//   - The boot race. qBittorrent can start before the tunnel is up. An unbound
//     session announces the guest's LAN address to trackers in that window; a
//     bound one has no interface to announce from and simply fails.
//   - The NordVPN daemon enforces its kill-switch itself, not through ufw, so
//     a daemon that dies takes the policy with it. The binding outlives it.
//
// Only the interface *name* is bound, never an address: the tunnel address is
// assigned by the provider at connect time and rotates on reconnect. libtorrent
// resolves the name to the current address itself and follows it across a
// re-address, which a baked-in address could not do.
//
// An empty iface leaves the session unbound — correct only when there is no
// VPN at all, where there is no interface to bind to and the guest routes over
// the LAN by design.
func qbittorrentConf(savePath, tempPath, iface string) string {
	if savePath == "" {
		savePath = "/downloads"
	}
	if tempPath == "" {
		tempPath = savePath + "/incomplete"
	}

	lines := []string{
		"[BitTorrent]",
		"Session\\DefaultSavePath=" + savePath,
		"Session\\TempPath=" + tempPath,
		"Session\\TempPathEnabled=true",

		// Encryption: forced (0=prefer, 1=force enabled, 2=force disabled)
		"Session\\Encryption=1",

		// Bandwidth — 0 means unlimited
		"Session\\GlobalDownloadSpeedLimit=0",
		"Session\\GlobalUploadSpeedLimit=0",
		"Session\\AlternativeGlobalDownloadSpeedLimit=0",
		"Session\\AlternativeGlobalUploadSpeedLimit=0",

		// Active torrent limits
		"Session\\MaxActiveDownloads=20",
		"Session\\MaxActiveUploads=20",
		"Session\\MaxActiveTorrents=40",

		// Connections — aggressive
		"Session\\MaxConnections=1000",
		"Session\\MaxConnectionsPerTorrent=100",
		"Session\\MaxUploads=40",
		"Session\\MaxUploadsPerTorrent=10",

		// Seeding limits: ratio 3.0, no time limit
		"Session\\MaxRatio=3",
		"Session\\MaxRatioAction=0",
		"Session\\MaxSeedingTime=-1",
		"Session\\MaxRatioEnabled=true",
		"Session\\MaxSeedingTimeEnabled=false",

		// Peer settings — aggressive
		"Session\\PeerTurnover=4",
		"Session\\PeerTurnoverCutoff=90",
		"Session\\PeerTurnoverInterval=30",

		// Disk
		"Session\\UseOSCache=true",
		"Session\\CoalesceReadWrite=true",

		// DHT, PeX, LSD for maximum peer discovery.
		"Session\\DHTEnabled=true",
		"Session\\PeXEnabled=true",
		"Session\\LSDEnabled=true",

		// Anonymous mode. Strips the client fingerprint from the peer ID,
		// sends a generic user-agent to trackers, withholds the configured
		// IP address, and omits the client version from the peer extension
		// handshake.
		//
		// On qBittorrent 2.9.0-3.2.5 (libtorrent < 1.0.0) this also killed
		// DHT, LSD and UPnP/NAT-PMP, which would have gutted the peer
		// discovery above. That behaviour moved to the separate "disable
		// connections not supported by proxies" option in 3.3.0, and every
		// base here installs 4.x or newer from the distro repos, so the two
		// settings no longer conflict: discovery stays on and the
		// fingerprint still goes away.
		//
		// This is defence in depth, not the primary control. The tunnel
		// binding and the kill-switch cover where you are; this covers who
		// you look like, which is what a tracker or peer logs alongside it.
		"Session\\AnonymousModeEnabled=true",

		// Announce to all trackers on each tier
		"Session\\AnnounceToAllTrackers=true",
		"Session\\AnnounceToAllTiers=true",

		// Verify HTTPS tracker certificates. Some builds default this off,
		// which turns an https:// tracker URL into an unauthenticated one.
		"Session\\ValidateHTTPSTrackerCertificate=true",
	}

	// IPv6 off, to match the kill-switch. The firewall drops IPv6 outright, so
	// every v6 peer and announce the session attempts is a connection that
	// cannot complete — wasted connection slots and announce timeouts rather
	// than a leak. Turning it off in the session keeps both layers saying the
	// same thing.
	lines = append(lines,
		"Session\\IPv6Enabled=false",

		// Fixed listen port, see qbittorrentListenPort.
		"Session\\Port="+strconv.Itoa(qbittorrentListenPort),
		"Session\\UseRandomPort=false",
	)

	if iface != "" {
		// Both keys, always together. Interface is the name qBittorrent shows
		// in its own UI and InterfaceName is what it hands to libtorrent;
		// setting one without the other leaves the UI and the live session
		// disagreeing about what is bound. InterfaceAddress is deliberately
		// absent — see the doc comment.
		lines = append(lines,
			"Session\\Interface="+iface,
			"Session\\InterfaceName="+iface,
		)
	}

	lines = append(lines,
		"",
		"[Preferences]",

		// Web UI on guest loopback only, port 8080.
		//
		// vee tunnel is the only access path. Under user-mode networking a
		// hostfwd binds 127.0.0.1 on the host and the request reaches
		// qBittorrent over guest loopback; on a bridge there is no hostfwd,
		// so the daemon's tunnel forwards over SSH to guest loopback
		// instead (see docs/tunnels.md). A
		// wildcard bind gains nothing — LocalHostAuth=false skips
		// authentication for loopback only, so a LAN client is answered 403 —
		// and in bridge mode the guest holds a real LAN address, where the
		// only thing keeping 8080 unreachable would be the kill-switch. Bind
		// narrowly instead of relying on the firewall to cover for it.
		"WebUI\\Address=127.0.0.1",
		"WebUI\\Port=8080",
		"WebUI\\LocalHostAuth=false",

		// Disable CSRF protection for LAN access
		"WebUI\\CSRFProtection=false",

		// vee tunnel proxies the WebUI through a random local port, so the
		// browser's Host header never matches port 8080 — validation would
		// 401 every tunnelled request.
		"WebUI\\HostHeaderValidation=false",
	)

	return strings.Join(lines, "\n") + "\n"
}
