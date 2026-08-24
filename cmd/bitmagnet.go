package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Benehiko/vee/internal/platform"
	"github.com/Benehiko/vee/internal/templates"
	"github.com/Benehiko/vee/internal/vm/build"
	"github.com/Benehiko/vee/internal/vpn"
)

// collectBitmagnetExtras gathers everything the bitmagnet template needs that
// cannot be derived from flags alone: the WireGuard configuration backing the
// kill-switch, and the host directory used as PostgreSQL's data directory.
//
// The database password is generated rather than prompted for. Nobody types it
// — bitmagnet reads it from its environment file inside the guest and the
// database never listens off-loopback — so asking the operator to invent one
// would only tempt a weak reused password.
func collectBitmagnetExtras() (*build.BitmagnetExtras, error) {
	password, err := templates.GeneratePGPassword()
	if err != nil {
		return nil, err
	}

	extras := &build.BitmagnetExtras{PGPassword: password}

	if createBitmagnetPGDir != "" {
		absDir, absErr := filepath.Abs(createBitmagnetPGDir)
		if absErr != nil {
			return nil, fmt.Errorf("resolve --pg-data-dir: %w", absErr)
		}
		// Create it rather than requiring it to exist: the common case is a
		// brand-new directory on a chosen disk, and virtiofs will not share a
		// path that is not there.
		if mkErr := os.MkdirAll(absDir, 0o700); mkErr != nil {
			return nil, fmt.Errorf("create PostgreSQL data directory: %w", mkErr)
		}
		// Refuse a network filesystem outright rather than letting it hang.
		// PostgreSQL's initdb fsyncs thousands of small files, and over
		// virtiofs onto NFS each one is a network round trip — a ten-second
		// initdb becomes hours. With NFS's default "hard" option it never even
		// errors: the guest blocks forever, cloud-init stalls mid-initdb, and
		// every later step (the firewall, the VPN tunnel) silently never runs.
		if fs := platform.NetworkFilesystemName(absDir); fs != "" {
			return nil, fmt.Errorf(
				"--pg-data-dir %s is on %s; PostgreSQL cannot run its data directory over a network filesystem "+
					"(initdb would stall indefinitely and the VM would never finish provisioning). "+
					"Use a directory on local storage", absDir, fs)
		}
		uid, gid, statErr := ownerOf(absDir)
		if statErr != nil {
			return nil, statErr
		}
		extras.PGDataHostDir = absDir
		extras.PGDataHostUID = uid
		extras.PGDataHostGID = gid
	}

	wgConf, err := promptBitmagnetVPN()
	if err != nil {
		return nil, err
	}
	extras.WireGuard = wgConf
	if wgConf != nil {
		extras.VPNProvider = "wireguard"
	}

	return extras, nil
}

// promptBitmagnetVPN asks for the WireGuard config that backs the guest's
// kill-switch.
//
// Only WireGuard is offered. The torrent template also supports the NordVPN
// snap, but this template runs on Alpine, which has no snapd. That is not a
// loss of coverage for NordVPN users: NordLynx is WireGuard, so a NordLynx
// config file exported from a NordVPN account works here directly.
//
// Declining the VPN is allowed, and turns the crawler off rather than merely
// warning about it. bitmagnet's DHT crawler continuously announces the guest to
// the swarm, so a VM without a tunnel would publish the host's real address to
// tens of thousands of peers — an outcome nobody picks deliberately by
// answering a prompt. The rest of the stack still runs, so the VM stays useful
// and adding a tunnel later is the only step needed to start crawling.
func promptBitmagnetVPN() (*vpn.WireGuardConfig, error) {
	if createBitmagnetWGConf != "" {
		return loadWireGuardConf(createBitmagnetWGConf)
	}
	if createBitmagnetNordToken != "" {
		return fetchNordLynxConf(createBitmagnetNordToken, createBitmagnetNordCountry)
	}

	stdin := bufio.NewReader(os.Stdin)
	fmt.Fprintln(os.Stderr, "bitmagnet crawls the BitTorrent DHT and announces this VM to the swarm.")
	fmt.Fprintln(os.Stderr, "A WireGuard tunnel with a kill-switch is strongly recommended.")
	fmt.Fprintln(os.Stderr, "NordVPN users: pass --nordvpn-token to fetch a NordLynx config automatically.")
	fmt.Fprint(os.Stderr, "Configure WireGuard? [Y/n]: ")

	answer, _ := stdin.ReadString('\n')
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "n") {
		fmt.Fprintln(os.Stderr,
			"No VPN configured: the DHT crawler is DISABLED so this VM cannot announce\n"+
				"your address to the swarm. The database and web UI still work.\n"+
				"Re-create with --wg-conf to start crawling.")
		return nil, nil
	}

	confPath, err := promptPath("Path to WireGuard .conf file: ")
	if err != nil {
		return nil, err
	}
	if confPath == "" {
		return nil, fmt.Errorf("a WireGuard config path is required (answer 'n' to run without a VPN)")
	}
	return loadWireGuardConf(confPath)
}

// fetchNordLynxConf builds a WireGuard config from a NordVPN account.
//
// NordVPN's own client is a snap and Alpine has no snapd, which is why this
// template takes WireGuard only. That is not a gap for NordVPN users though:
// NordLynx is WireGuard, so the account's NordLynx key plus a recommended
// server is a complete wg0.conf — and fetching it is friendlier than asking
// someone to export one from the dashboard by hand.
func fetchNordLynxConf(token, country string) (*vpn.WireGuardConfig, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Fprintln(os.Stderr, "Fetching NordLynx configuration from NordVPN...")
	conf, err := vpn.NordLynxConfig(ctx, token, country)
	if err != nil {
		return nil, err
	}
	// Report the endpoint: the kill-switch pins its handshake hole to this
	// address, so knowing which server was chosen is what makes the resulting
	// firewall rules intelligible.
	fmt.Fprintf(os.Stderr, "Using NordLynx endpoint %s\n", conf.Endpoint)
	return conf, nil
}

// loadWireGuardConf reads and parses a wg .conf file from the host.
func loadWireGuardConf(path string) (*vpn.WireGuardConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is user-supplied and intended to be read.
	if err != nil {
		return nil, fmt.Errorf("read WireGuard config: %w", err)
	}
	conf, err := vpn.ParseWireGuardConf(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse WireGuard config: %w", err)
	}
	return conf, nil
}
