package templates

import (
	"encoding/base64"
	"fmt"
	"net"
	"strings"

	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/internal/vpn"
)

// wgEndpointAddrsFile holds the WireGuard endpoint's resolved IPv4 addresses.
//
// The handshake hole in a kill-switched guest is pinned to these addresses, so
// they have to be known before the deny policy takes effect and still be known
// on every later boot, when there is no DNS left to look them up with. Writing
// them to disk is what carries the answer across the reboot.
const wgEndpointAddrsFile = "/etc/wireguard/endpoint-addrs"

// wgDefaultEndpointPort is the port assumed when an endpoint carries none.
const wgDefaultEndpointPort = 51820

// wireGuardEndpointHost extracts the host part of a WireGuard endpoint
// ("host:port"), which may be a hostname or an IPv4/IPv6 literal. The guest
// resolves it before the deny policy lands so the handshake hole can be pinned
// to real addresses.
func wireGuardEndpointHost(cfg *vpn.WireGuardConfig) string {
	if cfg == nil {
		return ""
	}
	ep := cfg.Endpoint
	// Bracketed IPv6 literal: [2001:db8::1]:51820
	if strings.HasPrefix(ep, "[") {
		if end := strings.Index(ep, "]"); end > 0 {
			return ep[1:end]
		}
	}
	if idx := strings.LastIndex(ep, ":"); idx >= 0 {
		return ep[:idx]
	}
	return ep
}

// wireGuardEndpointPort extracts the UDP port from a WireGuard endpoint
// ("host:port"), falling back to the standard 51820 when it cannot be parsed.
// The fallback is deliberately permissive: guessing wrong here only costs a
// tunnel that will not establish, whereas failing the build would reject a
// config that WireGuard itself accepts.
func wireGuardEndpointPort(cfg *vpn.WireGuardConfig) int {
	if cfg == nil {
		return wgDefaultEndpointPort
	}
	idx := strings.LastIndex(cfg.Endpoint, ":")
	if idx < 0 {
		return wgDefaultEndpointPort
	}
	port := 0
	if _, err := fmt.Sscanf(cfg.Endpoint[idx+1:], "%d", &port); err != nil || port <= 0 || port > 65535 {
		return wgDefaultEndpointPort
	}
	return port
}

// wgResolveEndpointCmd returns the command that resolves the endpoint to IPv4
// addresses and records them in wgEndpointAddrsFile.
//
// This must run before the deny policy takes effect. The config may name a
// hostname, and once outbound traffic is denied there is no DNS left to
// resolve it with — so the lookup has to happen while resolution still works
// and its result be baked into a file the boot-time rules can read back.
//
// An IP-literal endpoint resolves to itself, so the same command covers both
// shapes without a special case.
func wgResolveEndpointCmd(cfg *vpn.WireGuardConfig) string {
	return fmt.Sprintf(
		"mkdir -p /etc/wireguard && getent ahostsv4 %q | awk '{print $1}' | sort -u > %s",
		wireGuardEndpointHost(cfg), wgEndpointAddrsFile,
	)
}

// wgEndpointIsLiteralIP reports whether the endpoint names an IP address rather
// than a hostname. A literal cannot rotate, so guests configured with one need
// no refresh machinery at all.
func wgEndpointIsLiteralIP(cfg *vpn.WireGuardConfig) bool {
	return net.ParseIP(wireGuardEndpointHost(cfg)) != nil
}

// wgRefreshScriptPath is the endpoint-refresh script installed in guests whose
// endpoint is a hostname.
const wgRefreshScriptPath = "/usr/local/sbin/vee-wg-refresh-endpoint"

// wgRefreshEndpointScript returns a script that re-resolves a hostname endpoint
// and re-pins the handshake hole to the addresses that come back.
//
// The problem it solves: the handshake hole is pinned to the addresses resolved
// once, at build time, and written to wgEndpointAddrsFile. Nothing recomputed
// them. A provider that re-addresses its server therefore left wg-quick dialling
// the new IP while the firewall still permitted only the old one — the handshake
// was dropped, the tunnel never came back, and it stayed that way across
// reboots. That fails closed, so nothing leaked, but it is a silent permanent
// outage of the same class the pinning itself was introduced to prevent.
//
// Two details make this harder than "resolve again on boot":
//
//   - There is no DNS behind the kill-switch. Once the deny policy is up, a
//     lookup has nowhere to go, which is why the original resolve happens at
//     build time while ufw is still inactive. The script therefore opens a
//     hole to the configured nameservers for the duration of the lookup and
//     closes it again immediately, whatever the outcome. The hole is pinned to
//     those nameservers on port 53 rather than opened to the internet.
//   - wg-quick freezes the peer's address when it brings the interface up. Even
//     with the firewall corrected, an already-up interface keeps dialling the
//     old address, and the retry timer's "is wg0 up" guard sees a live
//     interface and does nothing. So a changed address must also force the
//     tunnel to re-read its own config.
//
// It fails closed at every step: if resolution fails, or returns nothing, the
// old pinned rules are left exactly as they were. A stale rule keeps a tunnel
// that was working working, whereas tearing the rules down first would open a
// window with no kill-switch at all.
//
// fw renders the firewall fragments for the base in use, so the same logic
// serves both ufw and iptables.
func wgRefreshEndpointScript(cfg *vpn.WireGuardConfig, fw wgFirewallCmds) string {
	return fmt.Sprintf(`#!/bin/sh
# Re-resolve the WireGuard endpoint and re-pin the handshake hole.
#
# Installed only for hostname endpoints. Managed by vee; edits are not preserved
# when the VM is recreated.
set -u

HOST=%q
PORT=%d
ADDRS=%q

# Nameservers to consult. Resolution has to survive the deny policy, so the
# lookup gets a temporary, narrowly-scoped hole rather than an always-open one.
nameservers() {
	resolvectl status 2>/dev/null | sed -n 's/.*DNS Servers: //p' | tr ' ' '\n'
	sed -n 's/^nameserver[[:space:]]*//p' /etc/resolv.conf 2>/dev/null
}

NS=$(nameservers | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | grep -v '^127\.' | sort -u)

for ns in $NS; do
	%s
done

NEW=$(getent ahostsv4 "$HOST" | awk '{print $1}' | sort -u)

for ns in $NS; do
	%s
done

# Fail closed: no answer means keep the addresses already pinned. A stale rule
# keeps a working tunnel working; dropping the rules would leave no kill-switch.
if [ -z "$NEW" ]; then
	exit 0
fi

OLD=$(cat "$ADDRS" 2>/dev/null || true)
if [ "$NEW" = "$OLD" ]; then
	exit 0
fi

# Install the new holes before withdrawing the old ones, so there is never a
# moment where the handshake has no way out.
for ip in $NEW; do
	%s
done

printf '%%s\n' "$NEW" > "$ADDRS"

for ip in $OLD; do
	if ! printf '%%s\n' "$NEW" | grep -qx "$ip"; then
		%s
	fi
done

# wg-quick pins the peer address when the interface comes up, so a corrected
# firewall is not enough on its own — the tunnel has to re-read its config.
%s
`,
		wireGuardEndpointHost(cfg),
		wireGuardEndpointPort(cfg),
		wgEndpointAddrsFile,
		fw.allowDNS,
		fw.denyDNS,
		fw.allowEndpoint,
		fw.denyEndpoint,
		fw.restartTunnel,
	)
}

// wgFirewallCmds renders the base-specific shell fragments the endpoint-refresh
// script is assembled from. ufw and iptables differ in how a rule is added and
// withdrawn, but the ordering the script depends on — open before withdraw,
// fail closed on a failed lookup — is identical, so only these fragments vary.
type wgFirewallCmds struct {
	// allowDNS opens a hole to "$ns" on port 53 for the lookup.
	allowDNS string
	// denyDNS withdraws it again.
	denyDNS string
	// allowEndpoint pins the handshake hole to "$ip".
	allowEndpoint string
	// denyEndpoint withdraws the hole for a no-longer-current "$ip".
	denyEndpoint string
	// restartTunnel forces wg-quick to re-read the endpoint.
	restartTunnel string
}

// wgRefreshWriteFile returns the cloud-init write-file carrying the
// endpoint-refresh script, or nil for an endpoint that cannot rotate.
//
// The script is shipped as a write-file rather than a "printf ... > file"
// runcmd because its content is a multi-line shell program full of quotes and
// backslashes. Squeezing that through a runcmd renders a YAML scalar that
// cloud-init parses as a mapping and discards the whole document — the exact
// failure TestTorrentUserDataIsValidYAML guards against. Base64 sidesteps
// quoting entirely.
func wgRefreshWriteFile(cfg *vpn.WireGuardConfig, fw wgFirewallCmds) *vm.CloudInitWriteFile {
	if wgEndpointIsLiteralIP(cfg) {
		return nil
	}
	return &vm.CloudInitWriteFile{
		Path:        wgRefreshScriptPath,
		Content:     base64.StdEncoding.EncodeToString([]byte(wgRefreshEndpointScript(cfg, fw))),
		Encoding:    "b64",
		Permissions: "0755",
	}
}

// wgDropIPv6Cmds returns the ip6tables rules that drop IPv6 outright.
//
// The tunnel is IPv4-only in practice. RenderWireGuardConf writes
// "AllowedIPs = 0.0.0.0/0, ::/0", but the Address it pairs that with is always
// an IPv4 /32 — no config path in vee ever assigns the interface an IPv6
// address, and the vee-managed server peers on an IPv4 /32 too. An interface
// with no IPv6 address cannot carry IPv6 traffic, so the "::/0" is aspirational
// rather than load-bearing.
//
// That left IPv6 outside the kill-switch entirely. The iptables bases install
// the ip6tables package but never gave it a rule, so its policy stayed at the
// default ACCEPT: on a network handing out IPv6 via router advertisements, a
// guest could reach the internet over IPv6 on the LAN interface while every
// IPv4 path was correctly denied. For the torrent and bitmagnet templates that
// is the exact leak the kill-switch exists to prevent — an announce over IPv6
// publishes the host's real address to the swarm just as effectively.
//
// Dropping IPv6 wholesale is the right shape here rather than mirroring the
// IPv4 holes: there is no IPv6 handshake to permit (the endpoint is resolved
// with getent ahostsv4), no IPv6 DHCP to keep alive, and SSH reaches the guest
// over IPv4. A policy of DROP with a loopback exception is therefore complete,
// and anything that later needs IPv6 has to add its hole deliberately.
//
// The loopback exception is not decorative: some software binds ::1 and would
// otherwise fail in ways that look nothing like a firewall problem.
func wgDropIPv6Cmds() []string {
	return []string{
		"ip6tables -P INPUT DROP",
		"ip6tables -P OUTPUT DROP",
		"ip6tables -P FORWARD DROP",
		// Flush anything the image shipped: a distro-provided ACCEPT rule left
		// in place would sit ahead of the policy and defeat it.
		"ip6tables -F",
		"ip6tables -A INPUT -i lo -j ACCEPT",
		"ip6tables -A OUTPUT -o lo -j ACCEPT",
	}
}

// wgPersistIPv6Cmds returns the commands that make the IPv6 policy survive a
// reboot.
//
// Alpine's iptables OpenRC service saves and restores the two families
// separately, so "/etc/init.d/iptables save" persists only the IPv4 table. A
// guest whose IPv6 rules were never saved comes back after a reboot with the
// IPv4 kill-switch intact and IPv6 wide open again — the leak reappearing
// silently, which is worse than never having closed it.
func wgPersistIPv6Cmds() []string {
	return []string{
		"/etc/init.d/ip6tables save",
		"rc-update add ip6tables default",
	}
}
