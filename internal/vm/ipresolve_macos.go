package vm

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// dhcpdLeasesPath is where the macOS built-in DHCP server (bootpd) records
// leases for NAT guests (Virtualization.framework and QEMU vmnet alike, all
// on bridge100).
const dhcpdLeasesPath = "/var/db/dhcpd_leases"

// resolveIPFromMACDarwin resolves a guest IP on a macOS host. NAT guests
// DHCP from the host's bootpd, so the freshest matching lease in
// /var/db/dhcpd_leases wins (the tart `tart ip` / lima vzNAT pattern). The
// ARP table (`arp -an`) is the fallback for bridged guests and cleared lease
// files. The file only appears once a guest actually DHCPs — callers poll.
func resolveIPFromMACDarwin(mac string) (string, error) {
	want, err := normalizeMAC(mac)
	if err != nil {
		return "", err
	}
	if data, readErr := os.ReadFile(dhcpdLeasesPath); readErr == nil {
		if ip, ok := lookupDHCPDLease(string(data), want); ok {
			return ip, nil
		}
	}
	out, arpErr := arpAnOutput()
	if arpErr != nil {
		return "", fmt.Errorf("no dhcpd lease for MAC %s and ARP scan failed: %w", mac, arpErr)
	}
	if ip, ok := lookupArpAn(out, want); ok {
		return ip, nil
	}
	return "", fmt.Errorf("no IP found for MAC %s in %s or the ARP table (has the guest booted and acquired a lease?)", mac, dhcpdLeasesPath)
}

// arpAnOutput runs the system arp tool. /usr/sbin is not always on PATH for
// launchd-started daemons, so the canonical location is tried first.
func arpAnOutput() (string, error) {
	arp := "/usr/sbin/arp"
	if _, err := os.Stat(arp); err != nil {
		p, lookErr := exec.LookPath("arp")
		if lookErr != nil {
			return "", lookErr
		}
		arp = p
	}
	//nolint:noctx,gosec // fixed args; called from exported ResolveIPFromMAC which carries no ctx
	out, err := exec.Command(arp, "-an").Output()
	return string(out), err
}

// lookupDHCPDLease scans bootpd's lease database: brace-delimited blocks of
// key=value lines. hw_address is "1,f6:38:41:1:e9:e6" — a hardware-type
// prefix, then octets WITHOUT leading zeros. DUID clients overwrite
// hw_address with identifier, so both fields are matched. When several
// blocks match (recreated VMs, stale 24h leases), the freshest lease=0x...
// expiry wins.
func lookupDHCPDLease(content, wantMAC string) (string, bool) {
	var bestIP string
	var bestLease uint64
	found := false

	var ip string
	var lease uint64
	match := false
	reset := func() { ip, lease, match = "", 0, false }
	flush := func() {
		if match && ip != "" && (!found || lease >= bestLease) {
			bestIP, bestLease, found = ip, lease, true
		}
		reset()
	}

	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		switch line {
		case "{":
			reset()
		case "}":
			flush()
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch k {
			case "ip_address":
				ip = v
			case "hw_address", "identifier":
				if m, err := normalizeMAC(stripHWType(v)); err == nil && m == wantMAC {
					match = true
				}
			case "lease":
				if n, err := strconv.ParseUint(strings.TrimPrefix(v, "0x"), 16, 64); err == nil {
					lease = n
				}
			}
		}
	}
	return bestIP, found
}

// stripHWType drops bootpd's leading hardware-type field ("1,aa:bb:…" → "aa:bb:…").
func stripHWType(s string) string {
	if _, rest, ok := strings.Cut(s, ","); ok {
		return rest
	}
	return s
}

// normalizeMAC canonicalizes a MAC for comparison: lowercase with per-octet
// leading zeros stripped. bootpd and macOS arp print "f6:38:41:1:e9:e6"
// while vee configs store "f6:38:41:01:e9:e6" — both normalize identically.
func normalizeMAC(s string) (string, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(s)), ":")
	if len(parts) != 6 {
		return "", fmt.Errorf("invalid MAC address %q", s)
	}
	for i, p := range parts {
		if p == "" || len(p) > 2 {
			return "", fmt.Errorf("invalid MAC address %q", s)
		}
		n, err := strconv.ParseUint(p, 16, 8)
		if err != nil {
			return "", fmt.Errorf("invalid MAC address %q: %w", s, err)
		}
		parts[i] = strconv.FormatUint(n, 16)
	}
	return strings.Join(parts, ":"), nil
}

// lookupArpAn parses `arp -an` lines of the form:
//
//	? (192.168.64.2) at f6:38:41:1:e9:e6 on bridge100 ifscope [ethernet]
func lookupArpAn(out, wantMAC string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		openIdx := strings.IndexByte(line, '(')
		closeIdx := strings.IndexByte(line, ')')
		atIdx := strings.Index(line, " at ")
		if openIdx < 0 || closeIdx <= openIdx || atIdx < closeIdx {
			continue
		}
		macField := line[atIdx+4:]
		if sp := strings.IndexByte(macField, ' '); sp > 0 {
			macField = macField[:sp]
		}
		m, err := normalizeMAC(macField)
		if err != nil {
			continue // "(incomplete)" entries and the like
		}
		if m == wantMAC {
			return line[openIdx+1 : closeIdx], true
		}
	}
	return "", false
}
