package templates

import "strings"

// qbittorrentConf returns the content of qBittorrent.conf pre-configured for:
//   - Forced encryption (no unencrypted connections)
//   - Aggressive peer settings (max connections, fast recheck)
//   - Unlimited bandwidth
//   - Seed ratio 3.0 before stopping
//   - 20 active downloads + 20 active uploads
//   - Save path set to savePath (e.g. /downloads)
func qbittorrentConf(savePath string) string {
	if savePath == "" {
		savePath = "/downloads"
	}

	lines := []string{
		"[BitTorrent]",
		"Session\\DefaultSavePath=" + savePath,
		"Session\\TempPath=" + savePath + "/incomplete",
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

		// Fast resume / recheck
		"Session\\UseOSCache=true",
		"Session\\CoalesceReadWrite=true",

		// DHT, PeX, LSD for maximum peer discovery
		"Session\\DHTEnabled=true",
		"Session\\PeXEnabled=true",
		"Session\\LSDEnabled=true",

		// Announce to all trackers on each tier
		"Session\\AnnounceToAllTrackers=true",
		"Session\\AnnounceToAllTiers=true",

		"",
		"[Preferences]",

		// Web UI on all interfaces, port 8080
		"WebUI\\Address=*",
		"WebUI\\Port=8080",
		"WebUI\\LocalHostAuth=false",

		// Disable CSRF protection for LAN access
		"WebUI\\CSRFProtection=false",
	}

	return strings.Join(lines, "\n") + "\n"
}
