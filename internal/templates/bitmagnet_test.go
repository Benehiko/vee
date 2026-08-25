package templates

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Benehiko/vee/internal/cloudinit"
	"github.com/Benehiko/vee/internal/vpn"
)

func testWireGuardConf() *vpn.WireGuardConfig {
	return &vpn.WireGuardConfig{
		PrivateKey: "aGVsbG8gd29ybGQgdGhpcyBpcyBub3QgYSBrZXk9",
		Address:    "10.5.0.2/32",
		DNS:        "10.5.0.1",
		Endpoint:   "vpn.example.com:51820",
		PublicKey:  "c2VydmVyIHB1YmxpYyBrZXkgcGxhY2Vob2xkZXI9",
	}
}

// indexOf returns the position of the first command containing sub, or -1.
func indexOf(cmds []string, sub string) int {
	for i, c := range cmds {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

// TestBitmagnetConfigIsValidYAML guards the hand-built config.yml: bitmagnet
// refuses to start on an unparseable config, which on a headless VM surfaces
// only as a dead service in the log.
func TestBitmagnetConfigIsValidYAML(t *testing.T) {
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(bitmagnetConfig("s3cret-pw", true)), &parsed); err != nil {
		t.Fatalf("bitmagnetConfig is not valid YAML: %v", err)
	}

	server, ok := parsed["http_server"].(map[string]any)
	if !ok {
		t.Fatal("config has no http_server section")
	}
	if addr, _ := server["local_address"].(string); addr != fmt.Sprintf(":%d", BitmagnetWebPort) {
		t.Errorf("http_server.local_address = %v, want :%d", server["local_address"], BitmagnetWebPort)
	}

	crawler, ok := parsed["dht_crawler"].(map[string]any)
	if !ok {
		t.Fatal("config has no dht_crawler section")
	}
	// Crawling is the whole point of the template; a VM that boots with the
	// crawler off indexes nothing and looks merely idle.
	if enabled, _ := crawler["enabled"].(bool); !enabled {
		t.Error("dht_crawler.enabled is false; the VM would index nothing")
	}
}

// TestBitmagnetKillSwitchDeniesByDefault is the security-critical test. The
// template exists so that bitmagnet's DHT traffic cannot escape the tunnel: if
// the OUTPUT policy is ever anything but DROP, the crawler leaks the guest's
// real address to the swarm the moment wg0 drops.
func TestBitmagnetKillSwitchDeniesByDefault(t *testing.T) {
	cmds := bitmagnetKillSwitchCmds(BitmagnetOptions{WireGuard: testWireGuardConf()})

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

	// ufw/iptables evaluate the policy against every packet, but the ordering
	// that matters here is that the tunnel comes up after the deny policy is
	// installed — otherwise there is a window where traffic leaves unprotected.
	denyIdx := indexOf(cmds, "iptables -P OUTPUT DROP")
	tunnelIdx := indexOf(cmds, "wg-quick up wg0")
	if denyIdx > tunnelIdx {
		t.Errorf("tunnel is brought up (index %d) before the deny policy (index %d); traffic could leak in between",
			tunnelIdx, denyIdx)
	}

	// The handshake must be allowed on the physical NIC, or the tunnel can
	// never establish and the VM is bricked into silence.
	if indexOf(cmds, "--dport 51820 -j ACCEPT") < 0 {
		t.Error("no outbound hole for the WireGuard handshake; the tunnel could never come up")
	}

	// SSH is the only management path into a kill-switched guest.
	if indexOf(cmds, "--sport 22") < 0 {
		t.Error("no outbound hole for SSH replies; the guest would be unreachable")
	}
}

// TestBitmagnetWebUINeverExposed pins the template's stance: the UI is reached
// through `vee tunnel` over SSH, never through a firewall hole. A rule opening
// the web port would put an unauthenticated UI on the LAN.
func TestBitmagnetWebUINeverExposed(t *testing.T) {
	for _, opts := range []BitmagnetOptions{
		{WireGuard: testWireGuardConf()},
		{}, // no VPN — the UI must still not be opened up
	} {
		cmds := bitmagnetKillSwitchCmds(opts)
		if idx := indexOf(cmds, fmt.Sprintf("--dport %d", BitmagnetWebPort)); idx >= 0 {
			t.Errorf("firewall opens the bitmagnet web port: %q", cmds[idx])
		}
	}
}

// TestBitmagnetKillSwitchWithoutVPN checks the no-VPN path still locks down
// inbound traffic. Without a tunnel there is nothing to fail closed to, so
// OUTPUT stays open — but the guest must not become an open host either.
func TestBitmagnetKillSwitchWithoutVPN(t *testing.T) {
	cmds := bitmagnetKillSwitchCmds(BitmagnetOptions{})

	if indexOf(cmds, "iptables -P INPUT DROP") < 0 {
		t.Error("inbound policy is not DROP even without a VPN")
	}
	if indexOf(cmds, "iptables -P OUTPUT DROP") >= 0 {
		t.Error("OUTPUT is denied with no tunnel configured; the guest could not reach anything at all")
	}
	if indexOf(cmds, "wg-quick") >= 0 {
		t.Error("wg-quick is invoked with no WireGuard config")
	}
}

// TestBitmagnetPostgresReusesExistingCluster is what makes the bind-mount
// worth having. Recreating the VM against a host directory that already holds
// a crawled index must reuse it; an unguarded initdb would refuse (or worse,
// a future change could wipe it), throwing away months of crawling.
func TestBitmagnetPostgresReusesExistingCluster(t *testing.T) {
	cmds := bitmagnetPostgresCmds(BitmagnetOptions{
		PGDataHostDir: "/mnt/tank/bitmagnet-pg",
		PGPassword:    "s3cret",
	})

	initIdx := indexOf(cmds, "initdb")
	if initIdx < 0 {
		t.Fatal("no initdb command emitted")
	}
	if !strings.Contains(cmds[initIdx], "PG_VERSION") {
		t.Errorf("initdb is not guarded by a PG_VERSION check: %q", cmds[initIdx])
	}

	// Role and database creation must be idempotent for the same reason.
	roleIdx := indexOf(cmds, "CREATE ROLE")
	if roleIdx < 0 {
		t.Fatal("no CREATE ROLE command emitted")
	}
	if !strings.Contains(cmds[roleIdx], "pg_roles") {
		t.Errorf("CREATE ROLE is not guarded against an existing role: %q", cmds[roleIdx])
	}
	dbIdx := indexOf(cmds, "createdb")
	if dbIdx < 0 {
		t.Fatal("no createdb command emitted")
	}
	if !strings.Contains(cmds[dbIdx], "pg_database") {
		t.Errorf("createdb is not guarded against an existing database: %q", cmds[dbIdx])
	}
}

// TestBitmagnetPostgresBindMountPersists checks the virtiofs mount is recorded
// in fstab. Cloud-init runcmds fire only on first boot, so without the fstab
// entry a reboot silently reverts the data directory to an empty local
// directory while the real index sits untouched on the host.
func TestBitmagnetPostgresBindMountPersists(t *testing.T) {
	cmds := bitmagnetPostgresCmds(BitmagnetOptions{
		PGDataHostDir: "/mnt/tank/bitmagnet-pg",
		PGDataHostUID: 1000,
		PGDataHostGID: 1000,
		PGPassword:    "s3cret",
	})

	if indexOf(cmds, "/etc/fstab") < 0 {
		t.Error("bind-mounted data directory is not recorded in /etc/fstab; it would not survive a reboot")
	}
	mountIdx := indexOf(cmds, "mount -t virtiofs")
	if mountIdx < 0 {
		t.Fatal("no virtiofs mount command emitted")
	}

	if initIdx := indexOf(cmds, "initdb"); initIdx < mountIdx {
		t.Error("initdb runs before the virtiofs mount lands; it would initialise the VM's own disk")
	}
	if indexOf(cmds, "chmod 0700") < 0 {
		t.Error("data directory mode is not set to 0700; initdb would refuse to run")
	}
}

// TestBitmagnetPostgresAdoptsHostOwnership covers the constraint that decides
// how the bind-mount works at all. virtiofs presents host ownership to the
// guest unchanged and vee runs virtiofsd unprivileged, so the guest CANNOT
// chown the share: the attempt fails with EPERM and initdb then refuses the
// directory it cannot access, leaving a half-initialised cluster and a dead
// database. The guest's postgres account is renumbered to the host owner
// instead — the reverse of the intuitive fix.
func TestBitmagnetPostgresAdoptsHostOwnership(t *testing.T) {
	cmds := bitmagnetPostgresCmds(BitmagnetOptions{
		PGDataHostDir: "/mnt/tank/bitmagnet-pg",
		PGDataHostUID: 1000,
		PGDataHostGID: 1000,
		PGPassword:    "s3cret",
	})

	if idx := indexOf(cmds, "chown postgres:postgres"); idx >= 0 {
		t.Errorf("chowns the virtiofs share, which fails with EPERM: %q", cmds[idx])
	}

	usermodIdx := indexOf(cmds, "usermod -u 1000")
	if usermodIdx < 0 {
		t.Fatal("guest postgres account is not renumbered to the host owner")
	}
	if indexOf(cmds, "groupmod -g 1000 postgres") < 0 {
		t.Error("guest postgres group is not renumbered to the host owner")
	}
	// The renumbering has to land before initdb, which checks that it owns the
	// data directory before writing anything into it.
	if initIdx := indexOf(cmds, "initdb"); initIdx < usermodIdx {
		t.Error("initdb runs before postgres is renumbered; it would not own the data directory")
	}

	// Without a bind-mount there is no host owner to adopt, and the ordinary
	// chown is both possible and correct.
	local := bitmagnetPostgresCmds(BitmagnetOptions{PGPassword: "s3cret"})
	if indexOf(local, "usermod -u") >= 0 {
		t.Error("guest postgres is renumbered even with no host directory shared")
	}
	if indexOf(local, "chown postgres:postgres") < 0 {
		t.Error("local data directory is not chowned to postgres")
	}
}

// TestBitmagnetPostgresLoopbackOnly pins the database to loopback. bitmagnet
// runs in the same guest, so there is no reason for PostgreSQL to be reachable
// over the network — and with the crawler's traffic on the same box, an
// exposed database is a real target.
func TestBitmagnetPostgresLoopbackOnly(t *testing.T) {
	cmds := bitmagnetPostgresCmds(BitmagnetOptions{PGPassword: "s3cret"})
	if indexOf(cmds, "listen_addresses = '127.0.0.1'") < 0 {
		t.Error("PostgreSQL is not pinned to loopback")
	}
}

// TestBitmagnetRunCmdOrdering covers the sequence that makes a first boot work
// at all: the install steps need the network, and the kill-switch closes it.
func TestBitmagnetRunCmdOrdering(t *testing.T) {
	cmds := bitmagnetRunCmds(BitmagnetOptions{
		WireGuard:  testWireGuardConf(),
		PGPassword: "s3cret",
	})

	denyIdx := indexOf(cmds, "iptables -P OUTPUT DROP")
	if denyIdx < 0 {
		t.Fatal("no OUTPUT deny policy in the full runcmd sequence")
	}
	for _, needsNetwork := range []string{"apk add", "curl -fsSL", "apk update"} {
		idx := indexOf(cmds, needsNetwork)
		if idx < 0 {
			t.Fatalf("runcmds missing %q", needsNetwork)
		}
		if idx > denyIdx {
			t.Errorf("%q runs after the kill-switch closes the network (index %d > %d)",
				needsNetwork, idx, denyIdx)
		}
	}

	// PostgreSQL must be accepting connections before bitmagnet starts: its
	// first run applies schema migrations, and a failed migration leaves the
	// service dead rather than retrying.
	pgIdx := indexOf(cmds, "rc-service postgresql start")
	bmIdx := indexOf(cmds, "rc-service bitmagnet start")
	if pgIdx < 0 || bmIdx < 0 {
		t.Fatal("postgresql or bitmagnet service start missing")
	}
	if pgIdx > bmIdx {
		t.Error("bitmagnet starts before PostgreSQL; its migrations would fail")
	}
	if indexOf(cmds, "pg_isready") < 0 {
		t.Error("no wait for PostgreSQL to accept connections before the role is created")
	}
}

// TestBitmagnetOpenRCService checks the init script wires the binary to the
// cloud-init-written config. Alpine has no systemd, so this hand-rolled unit is
// the only thing starting bitmagnet at boot.
func TestBitmagnetOpenRCService(t *testing.T) {
	svc := bitmagnetOpenRCService(true)
	for _, want := range []string{
		"#!/sbin/openrc-run",
		"/usr/local/bin/bitmagnet",
		// The working directory IS the configuration mechanism: bitmagnet reads
		// ./config.yml relative to its cwd and offers no flag or environment
		// variable to point it elsewhere. Getting this wrong is silent —
		// bitmagnet falls back to its defaults and reports the result as a
		// database login failure, not a missing config.
		fmt.Sprintf("directory=%q", bitmagnetConfigDir),
		// Supervised: bitmagnet panics and exits when PostgreSQL is not ready
		// yet, and a crawler that died hours ago looks the same as an idle one.
		`supervisor="supervise-daemon"`,
		"command_background=\"yes\"",
		"need net",
		"after firewall postgresql",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("OpenRC service missing %q", want)
		}
	}
	// The crawler talks to the entire internet; it must not do so as root.
	if !strings.Contains(svc, `command_user="bitmagnet:bitmagnet"`) {
		t.Error("bitmagnet does not drop privileges")
	}
	// The init script has to stay executable by OpenRC, so a password embedded
	// in it would be world-readable — strictly worse than the 0600 config.yml
	// the credentials actually live in.
	if strings.Contains(svc, "POSTGRES_PASSWORD") {
		t.Error("database password embedded in the world-readable init script")
	}
}

// TestBitmagnetConfigCarriesCredentials pins the database credentials into
// config.yml.
//
// They were originally written to an OpenRC /etc/conf.d file, which does not
// work: those are shell variables sourced by the init script, not exported
// into the service's environment. bitmagnet never saw them and fell back to
// its defaults, connecting as "postgres" with no password and panicking on
// every start.
//
// ssl_mode is equally load-bearing. bitmagnet requires TLS by default, and a
// local PostgreSQL with no certificate refuses the connection outright.
func TestBitmagnetConfigCarriesCredentials(t *testing.T) {
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(bitmagnetConfig("hunter2", true)), &parsed); err != nil {
		t.Fatalf("config with credentials is not valid YAML: %v", err)
	}

	pg, ok := parsed["postgres"].(map[string]any)
	if !ok {
		t.Fatal("config has no postgres section; bitmagnet would use its defaults")
	}
	for key, want := range map[string]any{
		"password": "hunter2",
		"user":     bitmagnetPGUser,
		"name":     bitmagnetPGDatabase,
		"host":     "127.0.0.1",
		"ssl_mode": "disable",
	} {
		if got := pg[key]; got != want {
			t.Errorf("postgres.%s = %v, want %v", key, got, want)
		}
	}
}

// TestWireGuardEndpointPort covers the parser behind the handshake firewall
// hole. Guessing the port wrong costs a tunnel that never establishes, so the
// non-standard-port case matters as much as the default.
func TestWireGuardEndpointPort(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     int
	}{
		{"standard port", "vpn.example.com:51820", 51820},
		{"custom port", "vpn.example.com:1194", 1194},
		{"ipv6 literal", "[2001:db8::1]:4500", 4500},
		{"no port", "vpn.example.com", 51820},
		{"garbage port", "vpn.example.com:notaport", 51820},
		{"out of range", "vpn.example.com:99999", 51820},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wireGuardEndpointPort(&vpn.WireGuardConfig{Endpoint: tc.endpoint})
			if got != tc.want {
				t.Errorf("wireGuardEndpointPort(%q) = %d, want %d", tc.endpoint, got, tc.want)
			}
		})
	}
	if got := wireGuardEndpointPort(nil); got != 51820 {
		t.Errorf("wireGuardEndpointPort(nil) = %d, want 51820", got)
	}
}

// TestGeneratePGPassword checks the generated password is shell-safe: it is
// interpolated into psql invocations and an OpenRC environment file, where a
// quote or a backslash would break the command rather than the password.
func TestGeneratePGPassword(t *testing.T) {
	seen := make(map[string]bool)
	for range 32 {
		pw, err := GeneratePGPassword()
		if err != nil {
			t.Fatalf("GeneratePGPassword: %v", err)
		}
		if len(pw) < 24 {
			t.Errorf("password %q is shorter than expected", pw)
		}
		if strings.ContainsAny(pw, "'\"\\$` \t\n") {
			t.Errorf("password %q contains characters that would break shell interpolation", pw)
		}
		if seen[pw] {
			t.Fatalf("GeneratePGPassword returned a duplicate: %q", pw)
		}
		seen[pw] = true
	}
}

// TestBitmagnetUserDataIsValidYAML renders the template's cloud-init exactly
// as vm.Manager does and parses it.
//
// This is a regression test for a failure that is silent and total. cloud-init
// renders each single-line runcmd as a bare YAML scalar, so a command starting
// with "[" — the ordinary shell `[ -f foo ]` test — parses as a flow sequence
// and invalidates the whole user-data document. cloud-init then discards every
// module: the guest boots with no packages, no SSH keys, no services, and the
// only evidence is one "Failed loading yaml blob" line in the serial log.
func TestBitmagnetUserDataIsValidYAML(t *testing.T) {
	opts := BitmagnetOptions{
		PGDataHostDir: "/mnt/tank/bitmagnet-pg",
		PGPassword:    "abc-XYZ_123",
		WireGuard:     testWireGuardConf(),
	}

	rendered, err := cloudinit.RenderUserData(&cloudinit.Config{
		Hostname:    "bitmagnet",
		DefaultUser: "alpine",
		SSHKeys:     []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAtest"},
		RunCmds:     bitmagnetRunCmds(opts),
		WriteFiles: []cloudinit.WriteFile{
			{Path: "/etc/bitmagnet/config.yml", Content: bitmagnetConfig(opts.PGPassword, opts.WireGuard != nil), Permissions: "0600"},
			{Path: "/etc/init.d/bitmagnet", Content: bitmagnetOpenRCService(true), Permissions: "0755"},
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

// TestBitmagnetCrawlerOffWithoutVPN is the behavioural half of the kill-switch.
//
// The firewall stops traffic that is already leaving; this stops it being
// generated at all. Crawling the DHT announces the guest's address to every
// peer it contacts, so with no tunnel configured the crawler must be off — a
// warning is not enough, because the damage is done by the time anyone reads
// the log. Both switches have to agree: dht_crawler.enabled gates the
// subsystem, and the dht_crawler worker key is what actually puts the guest on
// the DHT, so leaving the key in the service arguments would defeat the config.
func TestBitmagnetCrawlerOffWithoutVPN(t *testing.T) {
	var noVPN map[string]any
	if err := yaml.Unmarshal([]byte(bitmagnetConfig("pw", false)), &noVPN); err != nil {
		t.Fatalf("no-VPN config is not valid YAML: %v", err)
	}
	crawler, ok := noVPN["dht_crawler"].(map[string]any)
	if !ok {
		t.Fatal("config has no dht_crawler section")
	}
	if enabled, _ := crawler["enabled"].(bool); enabled {
		t.Error("DHT crawler is enabled with no VPN; the guest would announce its real address to the swarm")
	}
	if svc := bitmagnetOpenRCService(false); strings.Contains(svc, "--keys=dht_crawler") {
		t.Error("dht_crawler worker starts with no VPN; the guest would join the DHT regardless of the config switch")
	}

	// With a tunnel, crawling is the point of the template and must be on.
	var withVPN map[string]any
	if err := yaml.Unmarshal([]byte(bitmagnetConfig("pw", true)), &withVPN); err != nil {
		t.Fatalf("VPN config is not valid YAML: %v", err)
	}
	crawler, ok = withVPN["dht_crawler"].(map[string]any)
	if !ok {
		t.Fatal("config has no dht_crawler section")
	}
	if enabled, _ := crawler["enabled"].(bool); !enabled {
		t.Error("DHT crawler is disabled even with a VPN; the VM would index nothing")
	}
	if svc := bitmagnetOpenRCService(true); !strings.Contains(svc, "--keys=dht_crawler") {
		t.Error("dht_crawler worker missing with a VPN configured")
	}
}

// TestBitmagnetConfigTracksVPN pins the wiring between the two: the crawler
// switch is derived from whether a tunnel exists, not set independently, so the
// two cannot drift out of agreement.
func TestBitmagnetConfigTracksVPN(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts BitmagnetOptions
		want string
	}{
		{"no VPN", BitmagnetOptions{PGPassword: "pw"}, "enabled: false"},
		{"WireGuard", BitmagnetOptions{PGPassword: "pw", WireGuard: testWireGuardConf()}, "enabled: true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := bitmagnetConfig(tc.opts.PGPassword, tc.opts.WireGuard != nil)
			if !strings.Contains(got, tc.want) {
				t.Errorf("config missing %q for %s", tc.want, tc.name)
			}
		})
	}
}

// TestBitmagnetKillSwitchHolesAreNarrow is the test for the actual guarantee:
// with wg0 down, NO software in the guest can reach the internet.
//
// A default-deny OUTPUT policy is not sufficient on its own — the exceptions
// carved out of it decide whether the guarantee holds. Two obvious-looking
// rules quietly break it:
//
//   - "--dport 51820 -j ACCEPT" lets any process reach any host on the
//     internet that happens to listen on that port, tunnel down or not.
//   - "--sport 22 -j ACCEPT" lets any process bind source port 22 and open
//     new outbound connections, tunnel down or not.
//
// Both are usable covert channels, so each hole is pinned: the handshake to
// the endpoint's own addresses, and the SSH reply to conntrack ESTABLISHED.
func TestBitmagnetKillSwitchHolesAreNarrow(t *testing.T) {
	cmds := bitmagnetKillSwitchCmds(BitmagnetOptions{WireGuard: testWireGuardConf()})

	// The handshake hole must be destination-pinned, never a bare port hole.
	handshake := indexOf(cmds, "--dport 51820")
	if handshake < 0 {
		t.Fatal("no handshake rule emitted")
	}
	if !strings.Contains(cmds[handshake], "-d ") {
		t.Errorf("handshake hole is not pinned to the endpoint address: %q", cmds[handshake])
	}
	if indexOf(cmds, "/etc/wireguard/endpoint-addrs") < 0 {
		t.Error("endpoint addresses are never resolved; a hostname endpoint could not be pinned")
	}

	// The SSH reply hole must not admit new connections.
	ssh := indexOf(cmds, "--sport 22")
	if ssh < 0 {
		t.Fatal("no SSH reply rule emitted")
	}
	if !strings.Contains(cmds[ssh], "ESTABLISHED") {
		t.Errorf("SSH reply hole admits new outbound connections: %q", cmds[ssh])
	}

	// DHCP must not be a general UDP hole either.
	dhcp := indexOf(cmds, "--dport 67")
	if dhcp < 0 {
		t.Fatal("no DHCP rule emitted")
	}
	if !strings.Contains(cmds[dhcp], "255.255.255.255") {
		t.Errorf("DHCP hole is not restricted to the broadcast address: %q", cmds[dhcp])
	}

	// The crawler itself is rejected off-tunnel regardless of the above.
	owner := indexOf(cmds, "--gid-owner bitmagnet")
	if owner < 0 {
		t.Fatal("bitmagnet's own traffic is not restricted to the tunnel")
	}
	if !strings.Contains(cmds[owner], "! -o wg0") {
		t.Errorf("bitmagnet owner rule is not scoped to non-tunnel traffic: %q", cmds[owner])
	}
	// It has to come after the blanket wg0 accept, or tunnelled traffic dies too.
	if wg := indexOf(cmds, "-A OUTPUT -o wg0 -j ACCEPT"); wg > owner {
		t.Error("bitmagnet owner reject precedes the wg0 accept; tunnelled traffic would be rejected")
	}

	// The endpoint must be resolved before OUTPUT closes, since afterwards
	// there is no DNS left to resolve it with.
	resolve := indexOf(cmds, "/etc/wireguard/endpoint-addrs")
	if deny := indexOf(cmds, "iptables -P OUTPUT DROP"); resolve > deny {
		t.Error("endpoint is resolved after OUTPUT is denied; DNS would already be blocked")
	}
}

// TestWireGuardEndpointHost covers the parser feeding the pinned handshake
// rule. Getting the host wrong means the endpoint cannot be resolved and the
// tunnel never establishes.
func TestWireGuardEndpointHost(t *testing.T) {
	cases := []struct {
		endpoint string
		want     string
	}{
		{"vpn.example.com:51820", "vpn.example.com"},
		{"192.0.2.10:51820", "192.0.2.10"},
		{"[2001:db8::1]:4500", "2001:db8::1"},
		{"vpn.example.com", "vpn.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.endpoint, func(t *testing.T) {
			if got := wireGuardEndpointHost(&vpn.WireGuardConfig{Endpoint: tc.endpoint}); got != tc.want {
				t.Errorf("wireGuardEndpointHost(%q) = %q, want %q", tc.endpoint, got, tc.want)
			}
		})
	}
	if got := wireGuardEndpointHost(nil); got != "" {
		t.Errorf("wireGuardEndpointHost(nil) = %q, want empty", got)
	}
}

// TestBitmagnetEndpointRefreshInstalled covers the rotation path. The handshake
// hole is pinned to the addresses resolved once, at build time; nothing else
// recomputes them. Without a refresh a provider that re-addresses its server
// leaves wg-quick dialling the new IP while the firewall still permits only the
// old one — the handshake is dropped, the tunnel never comes back, and a reboot
// does not help because the stale addresses are what got persisted.
//
// That fails closed, so the crawler falls silent rather than leaking. But a
// silent permanent outage is the same class of failure the pinning was
// introduced to prevent, and it is the bug #151 fixed for the torrent bases.
func TestBitmagnetEndpointRefreshInstalled(t *testing.T) {
	cmds := bitmagnetKillSwitchCmds(BitmagnetOptions{WireGuard: testWireGuardConf()})

	if indexOf(cmds, wgRefreshScriptPath) < 0 {
		t.Fatal("no endpoint-refresh wiring; a rotated endpoint would strand the guest permanently")
	}

	// The boot hook has to re-resolve before the handshake is attempted, not
	// after it fails: a guest booting after a rotation should come up with a
	// working tunnel rather than waiting for a later cron tick.
	hook := indexOf(cmds, "/etc/local.d/wg0.start")
	if hook < 0 {
		t.Fatal("no boot hook emitted")
	}
	refreshPos := strings.Index(cmds[hook], wgRefreshScriptPath)
	upPos := strings.Index(cmds[hook], "wg-quick up wg0")
	if refreshPos < 0 || upPos < 0 || refreshPos > upPos {
		t.Errorf("boot hook does not re-resolve before bringing the tunnel up: %q", cmds[hook])
	}

	// A rotation while the guest is running needs picking up without a reboot.
	if indexOf(cmds, "/etc/periodic/1min/vee-wg-refresh") < 0 {
		t.Error("no periodic refresh; a rotation would not be noticed until the next reboot")
	}
	// Alpine ships crond stopped and out of the default runlevel, so dropping a
	// script into /etc/periodic alone would never run it.
	if indexOf(cmds, "rc-update add crond default") < 0 {
		t.Error("crond is never enabled; the periodic refresh would never fire")
	}
}

// TestBitmagnetBootHookNotOverwritten pins the ordering hazard. The refresh
// wiring installs its own /etc/local.d/wg0.start that runs the re-resolve ahead
// of "wg-quick up". A plain hook written unconditionally afterwards would
// clobber it and silently take the refresh back out on every boot — leaving the
// rotation bug in place while every other assertion still passed.
func TestBitmagnetBootHookNotOverwritten(t *testing.T) {
	cmds := bitmagnetKillSwitchCmds(BitmagnetOptions{WireGuard: testWireGuardConf()})

	var hooks []string
	for _, c := range cmds {
		if strings.Contains(c, "> /etc/local.d/wg0.start") {
			hooks = append(hooks, c)
		}
	}
	if len(hooks) != 1 {
		t.Fatalf("expected exactly one wg0.start writer, got %d: %q", len(hooks), hooks)
	}
	if !strings.Contains(hooks[0], wgRefreshScriptPath) {
		t.Errorf("the surviving boot hook is the plain one; the refresh is overwritten: %q", hooks[0])
	}
}

// TestBitmagnetLiteralEndpointSkipsRefresh checks the other branch. An IP
// literal cannot rotate, so the refresh machinery is pointless there — but the
// tunnel still needs a boot hook, or the guest comes back after a reboot with
// the kill-switch up and no tunnel.
func TestBitmagnetLiteralEndpointSkipsRefresh(t *testing.T) {
	conf := testWireGuardConf()
	conf.Endpoint = "203.0.113.5:51820"
	cmds := bitmagnetKillSwitchCmds(BitmagnetOptions{WireGuard: conf})

	if idx := indexOf(cmds, wgRefreshScriptPath); idx >= 0 {
		t.Errorf("refresh machinery installed for an endpoint that cannot rotate: %q", cmds[idx])
	}
	hook := indexOf(cmds, "/etc/local.d/wg0.start")
	if hook < 0 {
		t.Fatal("no boot hook for a literal endpoint; the tunnel would not survive a reboot")
	}
	if !strings.Contains(cmds[hook], "wg-quick up wg0") {
		t.Errorf("boot hook does not bring the tunnel up: %q", cmds[hook])
	}
}

// TestBitmagnetRefreshScriptShipped checks the script itself reaches the guest.
// The wiring only references a path; without the write-file carrying its
// content, every boot hook and cron tick would run a file that does not exist.
//
// The write-file helper is exercised directly rather than through
// NewBitmagnetConfig, which downloads a disk image — see the note above
// TestTorrentPackagesForBase.
func TestBitmagnetRefreshScriptShipped(t *testing.T) {
	conf := testWireGuardConf()
	wf := wgRefreshWriteFile(conf, alpineFirewallCmds(conf))
	if wf == nil {
		t.Fatal("the refresh script is never written; the boot hook would run a missing file")
	}
	if wf.Path != wgRefreshScriptPath {
		t.Errorf("refresh script path = %q, want %q", wf.Path, wgRefreshScriptPath)
	}
	if wf.Encoding != "b64" {
		t.Errorf("refresh script encoding = %q, want b64", wf.Encoding)
	}
	if wf.Permissions != "0755" {
		t.Errorf("refresh script permissions = %q, want 0755", wf.Permissions)
	}

	decoded, err := base64.StdEncoding.DecodeString(wf.Content)
	if err != nil {
		t.Fatalf("refresh script is not valid base64: %v", err)
	}
	// It has to be the iptables rendering, not ufw's: bitmagnet is Alpine-based
	// and has no ufw. A ufw-rendered script would fail every command silently
	// and never re-pin anything.
	script := string(decoded)
	if !strings.Contains(script, "iptables") {
		t.Error("refresh script is not rendered for the iptables base")
	}
	if strings.Contains(script, "ufw ") {
		t.Error("refresh script carries ufw commands, which Alpine does not have")
	}
	// The rules have to be persisted after a re-pin, or the corrected hole is
	// lost on the next boot and the guest returns to the stale-pin outage.
	if !strings.Contains(script, "/etc/init.d/iptables save") {
		t.Error("refresh script never persists the re-pinned rules")
	}
}

// TestBitmagnetIPv6IsDropped covers the family the kill-switch used to ignore.
// The image installs ip6tables but nothing ever gave it a rule, leaving the
// default ACCEPT policy: on a network advertising IPv6, the DHT crawler could
// announce the guest over IPv6 on the LAN interface while every IPv4 path was
// correctly denied. Publishing the host's real address to the swarm is exactly
// what this template exists to prevent, and the family it happens over makes
// no difference.
func TestBitmagnetIPv6IsDropped(t *testing.T) {
	// Both paths: the tunnel does not carry IPv6 either way.
	for _, tc := range []struct {
		name string
		opts BitmagnetOptions
	}{
		{"with wireguard", BitmagnetOptions{WireGuard: testWireGuardConf()}},
		{"without wireguard", BitmagnetOptions{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmds := bitmagnetKillSwitchCmds(tc.opts)

			for _, want := range []string{
				"ip6tables -P INPUT DROP",
				"ip6tables -P OUTPUT DROP",
				"ip6tables -P FORWARD DROP",
			} {
				if indexOf(cmds, want) < 0 {
					t.Errorf("IPv6 is not denied by default: missing %q", want)
				}
			}

			// The flush clears anything the image shipped, and has to precede
			// the loopback exceptions or it removes them again.
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

// TestBitmagnetIPv6PolicyPersists is the reboot half: Alpine saves the two
// families separately, so without an explicit ip6tables save the guest comes
// back with the IPv4 kill-switch intact and IPv6 open again.
func TestBitmagnetIPv6PolicyPersists(t *testing.T) {
	cmds := bitmagnetKillSwitchCmds(BitmagnetOptions{WireGuard: testWireGuardConf()})

	if indexOf(cmds, "/etc/init.d/ip6tables save") < 0 {
		t.Error("IPv6 rules are never saved; the policy is lost on reboot")
	}
	if indexOf(cmds, "rc-update add ip6tables default") < 0 {
		t.Error("the ip6tables service is not enabled; saved IPv6 rules would not be restored")
	}

	save := indexOf(cmds, "/etc/init.d/ip6tables save")
	if drop := indexOf(cmds, "ip6tables -P OUTPUT DROP"); drop > save {
		t.Error("the IPv6 policy is saved before it is installed; an empty table would persist")
	}
}

// TestBitmagnetWebUINeverExposedOverIPv6 pins the inbound half. The UI is
// reached through `vee tunnel` over SSH and never through a firewall hole; a
// v6 rule opening it would put an unauthenticated UI on the LAN just as a v4
// one would.
func TestBitmagnetWebUINeverExposedOverIPv6(t *testing.T) {
	for _, opts := range []BitmagnetOptions{
		{WireGuard: testWireGuardConf()},
		{},
	} {
		cmds := bitmagnetKillSwitchCmds(opts)
		for _, c := range cmds {
			if strings.HasPrefix(c, "ip6tables") &&
				strings.Contains(c, fmt.Sprintf("%d", BitmagnetWebPort)) {
				t.Errorf("IPv6 rule references the bitmagnet web port: %q", c)
			}
		}
	}
}
