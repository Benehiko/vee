package templates

import (
	"fmt"
	"strings"

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
