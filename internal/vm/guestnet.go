package vm

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/qemu"
)

// NetCheck statuses. Unlike HealthCheck's boolean, network checks need two
// extra outcomes: "info" for expected-but-notable states (a kill-switched
// guest legitimately answers no ARP) and "unavailable" for probes whose
// tooling is missing in the guest.
const (
	NetCheckPass        = "pass"
	NetCheckFail        = "fail"
	NetCheckInfo        = "info"
	NetCheckUnavailable = "unavailable"
)

// NetCheck is one judgment in a network report.
type NetCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// NetworkReport describes a running VM's network state from both sides of
// the virtualization boundary: what the host can see of the guest, and what
// the guest reports about its own firewall, VPN, DNS, and egress.
type NetworkReport struct {
	Host  HostNetwork  `json:"host"`
	Guest GuestNetwork `json:"guest"`
}

// HostNetwork is the host's view of the VM.
type HostNetwork struct {
	NICMode    string     `json:"nic_mode"`
	MAC        string     `json:"mac,omitempty"`
	ResolvedIP string     `json:"resolved_ip,omitempty"`
	SSHPort    int        `json:"ssh_port,omitempty"`
	QGA        bool       `json:"qga"`
	PublicIP   string     `json:"public_ip,omitempty"`
	Checks     []NetCheck `json:"checks"`
}

// GuestNetwork is the guest's view of its own networking.
type GuestNetwork struct {
	Interfaces   []qemu.GuestNetworkInterface `json:"interfaces,omitempty"`
	DefaultRoute string                       `json:"default_route,omitempty"`
	// PolicyRoute describes an fwmark policy-routing rule that steers traffic
	// into the VPN tunnel (e.g. "fwmark 0xe1f1 lookup 205 dev nordlynx").
	// NordLynx and wg-quick both route this way: the main table's default
	// route legitimately stays on the LAN NIC while every unmarked packet is
	// diverted to the tunnel's own table.
	PolicyRoute string        `json:"policy_route,omitempty"`
	DNSServers  []string      `json:"dns_servers,omitempty"`
	UFW         UFWState      `json:"ufw"`
	IPTables    IPTablesState `json:"iptables"`
	VPN         VPNState      `json:"vpn"`
	EgressIP    string        `json:"egress_ip,omitempty"`
	DNSEgressIP string        `json:"dns_egress_ip,omitempty"`
	Checks      []NetCheck    `json:"checks"`
}

// UFWState summarizes `ufw status verbose` inside the guest.
type UFWState struct {
	Available       bool   `json:"available"`
	Active          bool   `json:"active"`
	DefaultOutgoing string `json:"default_outgoing,omitempty"`
	RuleCount       int    `json:"rule_count"`
}

// IPTablesState summarizes the guest's iptables OUTPUT policy. Alpine guests
// (the bitmagnet template) have no ufw and enforce their kill-switch with
// iptables directly, so the firewall judgment has to read this instead.
type IPTablesState struct {
	Available bool `json:"available"`
	// OutputPolicy is the OUTPUT chain's default policy, "DROP" or "ACCEPT".
	OutputPolicy string `json:"output_policy,omitempty"`
	RuleCount    int    `json:"rule_count"`
}

// VPNState summarizes the guest's VPN tunnel, provider-specific: nordvpn
// status/settings for the NordVPN path, `wg show` for generic WireGuard.
type VPNState struct {
	Provider   string `json:"provider,omitempty"`
	Available  bool   `json:"available"`
	Connected  bool   `json:"connected"`
	Killswitch string `json:"killswitch,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// NetworkOptions tune QueryNetwork.
type NetworkOptions struct {
	// SkipEgress disables the external egress-IP lookups (one HTTPS request
	// from the guest, one from the host, one DNS query from the guest).
	SkipEgress bool
}

// guestUnavailable is echoed by guest probe scripts when every candidate
// command is missing or unrunnable, so both QGA and SSH transports degrade
// identically without turning a missing tool into an exec error.
const guestUnavailable = "VEE_UNAVAILABLE"

const cloudflareTraceURL = "https://www.cloudflare.com/cdn-cgi/trace"

// QueryNetwork gathers a full network report for a running VM: host-side
// visibility checks plus guest-side probes over QGA (SSH fallback). Probes
// whose tooling is missing degrade to "unavailable" checks; only a VM that
// is not running yields an error.
func (m *Manager) QueryNetwork(ctx context.Context, name string, opts NetworkOptions) (*NetworkReport, error) {
	cfg, err := m.loadConfig(name)
	if err != nil {
		return nil, err
	}
	state, err := m.loadState(name)
	if err != nil {
		return nil, err
	}
	if !state.Running {
		return nil, fmt.Errorf("VM %q is not running", name)
	}

	report := &NetworkReport{}
	m.hostNetwork(ctx, cfg, state, opts, &report.Host)
	m.guestNetwork(ctx, cfg, state, opts, &report.Host, &report.Guest)
	return report, nil
}

func (m *Manager) hostNetwork(ctx context.Context, cfg *VMConfig, state *VMState, opts NetworkOptions, host *HostNetwork) {
	host.NICMode = cfg.NIC.Mode
	if host.NICMode == "" {
		host.NICMode = "user"
	}
	host.MAC = cfg.NIC.MAC
	host.SSHPort = state.SSHPort
	host.QGA = state.QGASocket != ""

	// ARP/neighbour visibility. Only vz NAT and bridge-mode guests ever
	// appear in the host's neighbour table; user-mode slirp guests are
	// invisible by construction (see cmd/ip.go). A kill-switched torrent
	// guest also stops answering ARP while perfectly healthy.
	hostVisible := state.BackendName() == backend.VZ || cfg.NIC.Mode == "bridge"
	switch {
	case !hostVisible:
		host.Checks = append(host.Checks, NetCheck{
			Name: "host:arp", Status: NetCheckInfo,
			Detail: "user-mode NIC; guest is never visible on the LAN",
		})
	case cfg.NIC.MAC == "":
		host.Checks = append(host.Checks, NetCheck{
			Name: "host:arp", Status: NetCheckUnavailable,
			Detail: "no MAC recorded in config",
		})
	default:
		ip, err := ResolveIPFromMAC(cfg.NIC.MAC)
		switch {
		case err == nil:
			host.ResolvedIP = ip
			host.Checks = append(host.Checks, NetCheck{
				Name: "host:arp", Status: NetCheckPass,
				Detail: fmt.Sprintf("resolved %s", ip),
			})
		case cfg.VPNProvider != "":
			host.Checks = append(host.Checks, NetCheck{
				Name: "host:arp", Status: NetCheckInfo,
				Detail: "no ARP entry — expected: VPN kill-switch blocks LAN visibility",
			})
		default:
			host.Checks = append(host.Checks, NetCheck{
				Name: "host:arp", Status: NetCheckFail,
				Detail: fmt.Sprintf("MAC %s not in neighbour table", cfg.NIC.MAC),
			})
		}
	}

	if state.SSHPort > 0 {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(state.SSHPort))
		dialer := &net.Dialer{Timeout: 3 * time.Second}
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			host.Checks = append(host.Checks, NetCheck{
				Name: "host:ssh", Status: NetCheckPass,
				Detail: fmt.Sprintf("%s reachable", addr),
			})
		} else {
			host.Checks = append(host.Checks, NetCheck{
				Name: "host:ssh", Status: NetCheckFail,
				Detail: fmt.Sprintf("%s: %v", addr, err),
			})
		}
	} else {
		host.Checks = append(host.Checks, NetCheck{
			Name: "host:ssh", Status: NetCheckInfo,
			Detail: "no SSH port recorded",
		})
	}

	if state.QGASocket != "" {
		client, err := qemu.NewQGAClient(ctx, state.QGASocket, 5*time.Second)
		if err == nil {
			pingErr := client.GuestPing()
			_ = client.Close()
			if pingErr == nil {
				host.Checks = append(host.Checks, NetCheck{
					Name: "host:qga", Status: NetCheckPass,
					Detail: "guest agent responding",
				})
			} else {
				host.Checks = append(host.Checks, NetCheck{
					Name: "host:qga", Status: NetCheckFail,
					Detail: fmt.Sprintf("guest-ping: %v", pingErr),
				})
			}
		} else {
			host.Checks = append(host.Checks, NetCheck{
				Name: "host:qga", Status: NetCheckFail,
				Detail: fmt.Sprintf("connect: %v", err),
			})
		}
	} else {
		host.Checks = append(host.Checks, NetCheck{
			Name: "host:qga", Status: NetCheckInfo,
			Detail: "no guest agent socket",
		})
	}

	if !opts.SkipEgress {
		if ip, err := fetchPublicIP(ctx); err == nil {
			host.PublicIP = ip
		}
	}
}

//nolint:gocyclo // linear probe sequence; splitting it would scatter the report assembly
func (m *Manager) guestNetwork(ctx context.Context, cfg *VMConfig, state *VMState, opts NetworkOptions, host *HostNetwork, guest *GuestNetwork) {
	guest.VPN.Provider = cfg.VPNProvider

	if state.QGASocket == "" && state.SSHPort <= 0 {
		guest.Checks = append(guest.Checks, NetCheck{
			Name: "guest:probe", Status: NetCheckUnavailable,
			Detail: "no QGA socket or SSH port; guest state cannot be inspected",
		})
		return
	}

	probe := func(script string) (string, bool) {
		out, _, _, err := m.execGuestShell(ctx, cfg, state, script)
		out = strings.TrimSpace(out)
		if err != nil || out == guestUnavailable || out == "" {
			return "", false
		}
		return out, true
	}

	// Interfaces: QGA reports them natively; over SSH parse `ip -o addr`.
	if state.QGASocket != "" {
		if client, err := qemu.NewQGAClient(ctx, state.QGASocket, 5*time.Second); err == nil {
			if ifaces, ierr := client.GuestNetworkGetInterfaces(); ierr == nil {
				guest.Interfaces = ifaces
			}
			_ = client.Close()
		}
	}
	if guest.Interfaces == nil {
		if out, ok := probe("ip -o addr 2>/dev/null || echo " + guestUnavailable); ok {
			guest.Interfaces = ParseIPAddrBrief(out)
		}
	}

	if out, ok := probe("ip route show default 2>/dev/null || echo " + guestUnavailable); ok {
		via, dev := ParseDefaultRoute(out)
		if dev != "" {
			guest.DefaultRoute = strings.TrimSpace(fmt.Sprintf("via %s dev %s", via, dev))
			if via == "" {
				guest.DefaultRoute = "dev " + dev
			}
		}
	}

	// NordLynx and wg-quick never rewrite the main table's default route;
	// they add an fwmark rule diverting unmarked traffic to the tunnel's own
	// table. Read both rules and tables so the route check can recognize that.
	if cfg.VPNProvider != "" {
		if out, ok := probe("ip rule 2>/dev/null || echo " + guestUnavailable); ok {
			if fwmark, table := ParseIPRuleFwmark(out); table != "" {
				dev := ""
				if tout, tok := probe(fmt.Sprintf("ip route show table %s 2>/dev/null || echo %s", table, guestUnavailable)); tok {
					_, dev = ParseDefaultRoute(tout)
				}
				guest.PolicyRoute = strings.TrimSpace(fmt.Sprintf("fwmark %s lookup %s dev %s", fwmark, table, dev))
				if dev == "" {
					guest.PolicyRoute = fmt.Sprintf("fwmark %s lookup %s", fwmark, table)
				}
			}
		}
	}

	if out, ok := probe("resolvectl dns 2>/dev/null || cat /etc/resolv.conf 2>/dev/null || echo " + guestUnavailable); ok {
		guest.DNSServers = ParseDNSServers(out)
	}

	if out, ok := probe("ufw status verbose 2>/dev/null || sudo -n ufw status verbose 2>/dev/null || echo " + guestUnavailable); ok {
		guest.UFW = ParseUFWStatus(out)
	}

	// iptables, for guests with no ufw. -S prints the policy line ("-P OUTPUT
	// DROP") and every rule, and needs root: over QGA the probe already runs as
	// root, over the SSH fallback sudo -n covers a passwordless sudoer and
	// degrades to unavailable otherwise.
	if !guest.UFW.Available {
		if out, ok := probe("iptables -S 2>/dev/null || sudo -n iptables -S 2>/dev/null || echo " + guestUnavailable); ok {
			guest.IPTables = ParseIPTablesRules(out)
		}
	}

	switch vpnMechanism(cfg.VPNProvider) {
	case vpnMechNordVPN:
		if out, ok := probe("nordvpn status 2>/dev/null || echo " + guestUnavailable); ok {
			guest.VPN.Available = true
			connected, server, detail := ParseNordVPNStatus(out)
			guest.VPN.Connected = connected
			guest.VPN.Endpoint = server
			guest.VPN.Detail = detail
		}
		if out, ok := probe("nordvpn settings 2>/dev/null || echo " + guestUnavailable); ok {
			guest.VPN.Killswitch = ParseNordVPNSettings(out)
		}
	case vpnMechWireGuard:
		if out, ok := probe("wg show wg0 2>/dev/null || sudo -n wg show wg0 2>/dev/null || echo " + guestUnavailable); ok {
			guest.VPN.Available = true
			endpoint, handshake := ParseWGShow(out)
			guest.VPN.Endpoint = endpoint
			guest.VPN.Connected = handshake != ""
			guest.VPN.Detail = handshake
		}
	}

	if !opts.SkipEgress {
		// -4 everywhere: the host-side lookup is also forced to IPv4, and
		// comparing a v6 egress against a v4 host address is meaningless.
		script := fmt.Sprintf("curl -4fsS --max-time 5 %s 2>/dev/null || wget -4qO- -T 5 -t 1 %s 2>/dev/null || echo %s",
			cloudflareTraceURL, cloudflareTraceURL, guestUnavailable)
		if out, ok := probe(script); ok {
			guest.EgressIP = ParseCloudflareTrace(out)
		}
		// dig prints resolution errors to stdout, so only echo its output on
		// success; validate it parses as an IP before trusting it.
		if out, ok := probe(`o=$(dig -4 +short +time=3 +tries=1 CH TXT whoami.cloudflare @1.1.1.1 2>/dev/null) && echo "$o" || echo ` + guestUnavailable); ok {
			ip := strings.Trim(strings.TrimSpace(out), `"`)
			if net.ParseIP(ip) != nil {
				guest.DNSEgressIP = ip
			}
		}
	}

	guest.Checks = guestChecks(cfg, opts, host, guest)
}

// guestChecks derives pass/fail judgments from the gathered facts. VPN and
// firewall expectations only apply to VPN-configured (torrent) VMs; other
// templates get their facts reported without judgment.
//
//nolint:gocyclo // a rule table; each branch is one independent judgment
func guestChecks(cfg *VMConfig, opts NetworkOptions, host *HostNetwork, guest *GuestNetwork) []NetCheck {
	var checks []NetCheck
	add := func(name, status, detail string) {
		checks = append(checks, NetCheck{Name: name, Status: status, Detail: detail})
	}
	if cfg.VPNProvider == "" {
		return nil
	}

	mech := vpnMechanism(cfg.VPNProvider)

	tunnelDev := "wg0"
	if mech == vpnMechNordVPN {
		tunnelDev = "nordlynx"
	}

	// Traffic must leave through the tunnel device — either as the main
	// table's default route, or via an fwmark policy-routing rule diverting
	// into the tunnel's own table (how NordLynx and wg-quick actually route;
	// the main table's default route stays on the LAN NIC).
	switch {
	case strings.Contains(guest.PolicyRoute, "dev "+tunnelDev):
		add("guest:route", NetCheckPass, "policy-routed: "+guest.PolicyRoute)
	case guest.DefaultRoute == "":
		add("guest:route", NetCheckUnavailable, "default route not readable")
	case strings.Contains(guest.DefaultRoute, "dev "+tunnelDev):
		add("guest:route", NetCheckPass, guest.DefaultRoute)
	default:
		add("guest:route", NetCheckFail,
			fmt.Sprintf("%s — expected dev %s (no fwmark policy rule found)", guest.DefaultRoute, tunnelDev))
	}

	// DNS servers must not point at a LAN resolver (the classic leak). A LAN
	// entry alongside VPN-pushed resolvers is only notable, not a failure:
	// NordVPN leaves the DHCP resolver on the link and injects its own, and
	// the kill-switch blocks the LAN one anyway — the dns-egress check below
	// is the ground truth for where queries actually exit.
	switch {
	case len(guest.DNSServers) == 0:
		add("guest:dns-leak", NetCheckUnavailable, "DNS configuration not readable")
	default:
		lans := lanNetworks(guest.Interfaces)
		var lanHits, publicHits []string
		loopback := ""
		for _, s := range guest.DNSServers {
			ip := net.ParseIP(s)
			if ip == nil {
				continue
			}
			if ip.IsLoopback() {
				loopback = s
				continue
			}
			inLAN := false
			for _, lan := range lans {
				if lan.Contains(ip) {
					inLAN = true
				}
			}
			if inLAN {
				lanHits = append(lanHits, s)
			} else {
				publicHits = append(publicHits, s)
			}
		}
		switch {
		case len(lanHits) > 0 && len(publicHits) > 0:
			add("guest:dns-leak", NetCheckInfo, fmt.Sprintf(
				"%s is on the LAN but VPN DNS %s is also configured; see guest:dns-egress",
				strings.Join(lanHits, ", "), strings.Join(publicHits, ", ")))
		case len(lanHits) > 0:
			add("guest:dns-leak", NetCheckFail, lanHits[0]+" is the LAN resolver — DNS bypasses the VPN")
		case loopback != "":
			add("guest:dns-leak", NetCheckInfo, loopback+" is a local resolver stub; upstream not verified")
		default:
			add("guest:dns-leak", NetCheckPass, strings.Join(guest.DNSServers, ", "))
		}
	}

	// Firewall. Two front-ends are in play: the torrent template's Ubuntu
	// guests use ufw, and the bitmagnet template's Alpine guests have no ufw
	// and drive iptables directly. Judge whichever one the guest actually has,
	// rather than reporting a correctly-firewalled Alpine guest as
	// "ufw not readable".
	switch {
	case guest.UFW.Available && !guest.UFW.Active:
		add("guest:firewall", NetCheckFail, "ufw inactive")
	case guest.UFW.Available:
		add("guest:firewall", NetCheckPass,
			fmt.Sprintf("ufw active, outgoing=%s (%d rules)", guest.UFW.DefaultOutgoing, guest.UFW.RuleCount))
	case guest.IPTables.Available:
		add("guest:firewall", NetCheckPass,
			fmt.Sprintf("iptables, OUTPUT policy %s (%d rules)", guest.IPTables.OutputPolicy, guest.IPTables.RuleCount))
	default:
		add("guest:firewall", NetCheckUnavailable, "no ufw or iptables state readable in guest")
	}

	// Kill-switch, for the WireGuard mechanism only: NordVPN enforces its own
	// inside the daemon and is judged from `nordvpn settings` below. Both
	// front-ends express the same rule — deny egress by default so that a
	// tunnel that never came up (or dropped) silences the guest instead of
	// letting it fall back to the LAN.
	if mech == vpnMechWireGuard {
		switch {
		case guest.UFW.Available && guest.UFW.Active:
			if guest.UFW.DefaultOutgoing == "deny" {
				add("guest:killswitch", NetCheckPass, "ufw default deny (outgoing)")
			} else {
				add("guest:killswitch", NetCheckFail,
					fmt.Sprintf("ufw default outgoing is %q, expected deny", guest.UFW.DefaultOutgoing))
			}
		case guest.IPTables.Available:
			if guest.IPTables.OutputPolicy == "DROP" {
				add("guest:killswitch", NetCheckPass, "iptables OUTPUT policy DROP")
			} else {
				add("guest:killswitch", NetCheckFail,
					fmt.Sprintf("iptables OUTPUT policy is %q, expected DROP", guest.IPTables.OutputPolicy))
			}
		default:
			add("guest:killswitch", NetCheckUnavailable, "no firewall state readable in guest")
		}
	}

	// VPN connection.
	switch {
	case !guest.VPN.Available:
		add("guest:vpn", NetCheckUnavailable, cfg.VPNProvider+" state not readable in guest")
	case guest.VPN.Connected:
		detail := guest.VPN.Endpoint
		if detail == "" {
			detail = guest.VPN.Detail
		}
		add("guest:vpn", NetCheckPass, strings.TrimSpace(cfg.VPNProvider+" connected "+detail))
	default:
		add("guest:vpn", NetCheckFail, cfg.VPNProvider+" not connected: "+guest.VPN.Detail)
	}
	if mech == vpnMechNordVPN {
		switch guest.VPN.Killswitch {
		case "on":
			add("guest:killswitch", NetCheckPass, "nordvpn kill switch enabled")
		case "off":
			add("guest:killswitch", NetCheckFail, "nordvpn kill switch disabled")
		default:
			add("guest:killswitch", NetCheckUnavailable, "nordvpn settings not readable")
		}
	}

	if opts.SkipEgress {
		return checks
	}

	// Egress IP: must be public and differ from the host's public IP.
	switch {
	case guest.EgressIP == "":
		add("guest:egress-ip", NetCheckUnavailable, "egress lookup failed (no curl/wget or no connectivity)")
	case !isPublicIP(guest.EgressIP):
		add("guest:egress-ip", NetCheckFail, guest.EgressIP+" is not a public address")
	case host.PublicIP != "" && guest.EgressIP == host.PublicIP:
		add("guest:egress-ip", NetCheckFail,
			guest.EgressIP+" matches the host's public IP — traffic is not tunnelled")
	case host.PublicIP == "":
		add("guest:egress-ip", NetCheckInfo,
			guest.EgressIP+" (host public IP unknown; could not compare)")
	default:
		add("guest:egress-ip", NetCheckPass,
			fmt.Sprintf("%s differs from host %s", guest.EgressIP, host.PublicIP))
	}

	// DNS egress: the resolver Cloudflare sees must not be the host's IP.
	switch {
	case guest.DNSEgressIP == "":
		add("guest:dns-egress", NetCheckUnavailable, "whoami.cloudflare lookup failed (dig missing or port 53 blocked)")
	case host.PublicIP != "" && guest.DNSEgressIP == host.PublicIP:
		add("guest:dns-egress", NetCheckFail,
			guest.DNSEgressIP+" matches the host's public IP — DNS queries leak outside the VPN")
	case host.PublicIP == "":
		add("guest:dns-egress", NetCheckInfo,
			guest.DNSEgressIP+" (host public IP unknown; could not compare)")
	default:
		add("guest:dns-egress", NetCheckPass,
			fmt.Sprintf("%s differs from host %s", guest.DNSEgressIP, host.PublicIP))
	}

	return checks
}

// lanNetworks derives the guest's on-link IPv4 networks from its non-tunnel,
// non-loopback interfaces, for the DNS-leak judgment.
func lanNetworks(ifaces []qemu.GuestNetworkInterface) []*net.IPNet {
	var nets []*net.IPNet
	for _, iface := range ifaces {
		if iface.Name == "lo" || isTunnelDev(iface.Name) {
			continue
		}
		for _, a := range iface.IPAddresses {
			ip := net.ParseIP(a.IPAddress)
			if ip == nil || ip.To4() == nil {
				continue
			}
			mask := net.CIDRMask(a.Prefix, 32)
			nets = append(nets, &net.IPNet{IP: ip.Mask(mask), Mask: mask})
		}
	}
	return nets
}

func isTunnelDev(name string) bool {
	return name == "nordlynx" || strings.HasPrefix(name, "wg") || strings.HasPrefix(name, "tun")
}

func isPublicIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}

// fetchPublicIP asks Cloudflare's trace endpoint for the host's public IP.
func fetchPublicIP(ctx context.Context) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, cloudflareTraceURL, nil)
	if err != nil {
		return "", err
	}
	// Force IPv4 so the result is comparable with the guest's -4 lookups.
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp4", addr)
		},
	}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	ip := ParseCloudflareTrace(string(body))
	if ip == "" {
		return "", fmt.Errorf("no ip= line in trace response")
	}
	return ip, nil
}

// ---- parsers ----

// ParseDefaultRoute extracts gateway and device from `ip route show default`
// output, e.g. "default via 192.168.1.1 dev enp1s0 proto dhcp" or
// "default dev nordlynx table 205 scope link".
func ParseDefaultRoute(output string) (via, dev string) {
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "default" {
			continue
		}
		for i := 0; i < len(fields)-1; i++ {
			switch fields[i] {
			case "via":
				via = fields[i+1]
			case "dev":
				dev = fields[i+1]
			}
		}
		if dev != "" {
			return via, dev
		}
	}
	return via, dev
}

// ParseIPRuleFwmark finds the VPN's fwmark diversion rule in `ip rule`
// output — e.g. NordLynx's "32765: not from all fwmark 0xe1f1 lookup 205" or
// wg-quick's "32764: not from all fwmark 0xca6c lookup 51820" — and returns
// the mark and the routing table it diverts to.
func ParseIPRuleFwmark(output string) (fwmark, table string) {
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		for i := 0; i < len(fields)-1; i++ {
			switch fields[i] {
			case "fwmark":
				fwmark = fields[i+1]
			case "lookup":
				table = fields[i+1]
			}
		}
		if fwmark != "" && table != "" && table != "main" && table != "local" && table != "default" {
			return fwmark, table
		}
		fwmark, table = "", ""
	}
	return "", ""
}

// ParseDNSServers extracts nameserver IPs from either `resolvectl dns`
// output ("Global: 1.1.1.1", "Link 2 (enp1s0): 192.168.1.1") or an
// /etc/resolv.conf ("nameserver 1.1.1.1").
func ParseDNSServers(output string) []string {
	var servers []string
	seen := map[string]bool{}
	resolvConf := strings.Contains(output, "nameserver")
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if resolvConf && fields[0] != "nameserver" {
			continue
		}
		for _, f := range fields {
			// resolvectl may suffix an interface ("1.1.1.1%enp1s0").
			f, _, _ = strings.Cut(f, "%")
			if ip := net.ParseIP(f); ip != nil && !seen[f] {
				seen[f] = true
				servers = append(servers, f)
			}
		}
	}
	return servers
}

// ParseUFWStatus parses `ufw status verbose` output.
func ParseUFWStatus(output string) UFWState {
	state := UFWState{Available: true}
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Status:"):
			state.Active = strings.Contains(line, "active") && !strings.Contains(line, "inactive")
		case strings.HasPrefix(line, "Default:"):
			// "Default: deny (incoming), deny (outgoing), disabled (routed)"
			for part := range strings.SplitSeq(strings.TrimPrefix(line, "Default:"), ",") {
				part = strings.TrimSpace(part)
				if policy, ok := strings.CutSuffix(part, " (outgoing)"); ok {
					state.DefaultOutgoing = policy
				}
			}
		case strings.Contains(line, "ALLOW") || strings.Contains(line, "DENY") ||
			strings.Contains(line, "REJECT") || strings.Contains(line, "LIMIT"):
			state.RuleCount++
		}
	}
	return state
}

// ParseIPTablesRules parses `iptables -S` output: the "-P CHAIN POLICY" lines
// carry the chain defaults and the "-A" lines are the rules.
func ParseIPTablesRules(output string) IPTablesState {
	state := IPTablesState{Available: true}
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 3 && fields[0] == "-P" && fields[1] == "OUTPUT":
			state.OutputPolicy = fields[2]
		case len(fields) > 0 && fields[0] == "-A":
			state.RuleCount++
		}
	}
	return state
}

// sanitizeNordVPNLine strips the spinner characters and carriage returns the
// nordvpn CLI prepends to its output lines.
func sanitizeNordVPNLine(line string) string {
	return strings.TrimLeft(strings.TrimSpace(line), "-\\|/\r ")
}

// ParseNordVPNStatus parses `nordvpn status` output.
func ParseNordVPNStatus(output string) (connected bool, server, detail string) {
	var parts []string
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		line = sanitizeNordVPNLine(line)
		switch {
		case strings.HasPrefix(line, "Status:"):
			connected = strings.Contains(line, "Connected")
			parts = append(parts, line)
		case strings.HasPrefix(line, "Hostname:"):
			server = strings.TrimSpace(strings.TrimPrefix(line, "Hostname:"))
		case strings.HasPrefix(line, "Current server:"):
			server = strings.TrimSpace(strings.TrimPrefix(line, "Current server:"))
		case strings.HasPrefix(line, "Country:"), strings.HasPrefix(line, "IP:"),
			strings.HasPrefix(line, "Current technology:"):
			parts = append(parts, line)
		}
	}
	if len(parts) == 0 {
		// No recognizable status line — e.g. "We couldn't reach System
		// Daemon." Surface the raw first line instead of silence.
		for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
			if line = sanitizeNordVPNLine(line); line != "" {
				return false, "", line
			}
		}
	}
	return connected, server, strings.Join(parts, "; ")
}

// ParseNordVPNSettings extracts the kill-switch state ("on"/"off"/"") from
// `nordvpn settings` output.
func ParseNordVPNSettings(output string) string {
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		line = sanitizeNordVPNLine(line)
		if !strings.HasPrefix(line, "Kill Switch:") {
			continue
		}
		if strings.Contains(line, "enabled") || strings.Contains(line, "on") {
			return "on"
		}
		return "off"
	}
	return ""
}

// ParseWGShow parses `wg show <dev>` output, returning the peer endpoint and
// the latest-handshake text (empty when no handshake has completed — i.e.
// the tunnel never came up).
func ParseWGShow(output string) (endpoint, handshake string) {
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "endpoint:"); ok {
			endpoint = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(line, "latest handshake:"); ok {
			handshake = "handshake " + strings.TrimSpace(v)
		}
	}
	return endpoint, handshake
}

// ParseCloudflareTrace extracts the ip= value from a cdn-cgi/trace response.
func ParseCloudflareTrace(output string) string {
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ip="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ParseIPAddrBrief parses `ip -o addr` output into guest interface entries
// (the SSH fallback when QGA's guest-network-get-interfaces is unavailable).
// Line format: "2: enp1s0    inet 192.168.1.5/24 brd 192.168.1.255 ...".
func ParseIPAddrBrief(output string) []qemu.GuestNetworkInterface {
	byName := map[string]*qemu.GuestNetworkInterface{}
	var order []string
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		name := strings.TrimSuffix(fields[1], ":")
		family := fields[2]
		if family != "inet" && family != "inet6" {
			continue
		}
		addr, prefixStr, ok := strings.Cut(fields[3], "/")
		if !ok {
			continue
		}
		prefix, err := strconv.Atoi(prefixStr)
		if err != nil {
			continue
		}
		iface, exists := byName[name]
		if !exists {
			iface = &qemu.GuestNetworkInterface{Name: name}
			byName[name] = iface
			order = append(order, name)
		}
		ipType := "ipv4"
		if family == "inet6" {
			ipType = "ipv6"
		}
		iface.IPAddresses = append(iface.IPAddresses, qemu.GuestIPAddress{
			IPAddress: addr, Prefix: prefix, IPAddressType: ipType,
		})
	}
	ifaces := make([]qemu.GuestNetworkInterface, 0, len(order))
	for _, name := range order {
		ifaces = append(ifaces, *byName[name])
	}
	return ifaces
}

// VPN mechanisms. The vpn_provider recorded in a VM config names the *source*
// of the tunnel (which the operator chose), not the *mechanism* used to
// inspect it. Several provider names map onto the same mechanism: "generic",
// "wireguard" and "nordlynx" are all plain WireGuard interfaces probed with
// `wg show`, because NordLynx is WireGuard and the bitmagnet template renders
// a wg0.conf for it rather than installing NordVPN's client.
//
// Probing keys off the mechanism, never the provider string. Matching the raw
// provider is what made `vee network` skip the tunnel probe entirely for
// bitmagnet VMs (recorded as "wireguard"/"nordlynx"): the report still
// rendered its route and egress checks, so a dead VPN check read as a healthy
// VM.
const (
	vpnMechNone      = ""
	vpnMechNordVPN   = "nordvpn"   // the NordVPN snap daemon, probed via `nordvpn status`
	vpnMechWireGuard = "wireguard" // a wg interface, probed via `wg show wg0`
)

// vpnMechanism maps a recorded vpn_provider onto the mechanism used to probe
// it. An unrecognized non-empty provider is treated as WireGuard: every
// provider vee configures other than the NordVPN snap renders a wg0.conf, so
// that is both the safe default and the correct one for a future provider
// added without updating this function.
func vpnMechanism(provider string) string {
	switch provider {
	case "":
		return vpnMechNone
	case "nordvpn":
		return vpnMechNordVPN
	default:
		return vpnMechWireGuard
	}
}
