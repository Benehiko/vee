package vpn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	nordAPI        = "https://api.nordvpn.com/v1"
	nordAPITimeout = 15 * time.Second
)

// NordVPNConfig holds the NordVPN access token and optional country used to
// configure the nordvpn snap inside the VM on first boot.
type NordVPNConfig struct {
	Token   string // from my.nordaccount.com/dashboard/nordvpn/access-tokens/
	Country string // optional, e.g. "Germany" — passed to nordvpn connect
}

// WireGuardConfig holds the data needed to render a wg0.conf.
type WireGuardConfig struct {
	PrivateKey string
	Address    string // assigned IP, e.g. 10.5.0.2/32
	DNS        string
	Endpoint   string // host:port
	PublicKey  string // server pubkey
}

// ParseWireGuardConf parses a minimal wg0.conf into a WireGuardConfig.
func ParseWireGuardConf(content string) (*WireGuardConfig, error) {
	cfg := &WireGuardConfig{}
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "PrivateKey":
			cfg.PrivateKey = v
		case "Address":
			cfg.Address = v
		case "DNS":
			cfg.DNS = v
		case "PublicKey":
			cfg.PublicKey = v
		case "Endpoint":
			cfg.Endpoint = v
		}
	}
	if cfg.PrivateKey == "" || cfg.PublicKey == "" || cfg.Endpoint == "" {
		return nil, fmt.Errorf("WireGuard config missing required fields (PrivateKey, PublicKey, Endpoint)")
	}
	return cfg, nil
}

// RenderWireGuardConf renders a wg0.conf file content from the given config.
func RenderWireGuardConf(cfg *WireGuardConfig) string {
	var sb strings.Builder
	sb.WriteString("[Interface]\n")
	fmt.Fprintf(&sb, "PrivateKey = %s\n", cfg.PrivateKey)
	fmt.Fprintf(&sb, "Address = %s\n", cfg.Address)
	fmt.Fprintf(&sb, "DNS = %s\n", cfg.DNS)
	sb.WriteString("\n[Peer]\n")
	fmt.Fprintf(&sb, "PublicKey = %s\n", cfg.PublicKey)
	sb.WriteString("AllowedIPs = 0.0.0.0/0, ::/0\n")
	fmt.Fprintf(&sb, "Endpoint = %s\n", cfg.Endpoint)
	sb.WriteString("PersistentKeepalive = 25\n")
	return sb.String()
}

// ValidateToken checks that a NordVPN access token is non-empty and can reach
// the API. It does NOT call CF-blocked endpoints — it hits /v1/servers/countries
// which is public and unauthenticated, just to verify network reachability.
// Token format validation is purely syntactic (non-empty).
func ValidateToken(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("NordVPN access token is empty")
	}
	// Authenticate against a real credentials request rather than pinging a
	// public endpoint: the previous check fetched /servers/countries and
	// discarded the response, so it reported success for any non-empty string
	// and the first sign of a bad token was a guest that could not connect.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nordAPIBase+"/users/services/credentials", nil)
	if err != nil {
		return fmt.Errorf("build NordVPN request: %w", err)
	}
	req.SetBasicAuth("token", token)

	hc := &http.Client{Timeout: nordAPITimeout}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("NordVPN API unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("NordVPN rejected the access token; generate a new one at " +
			"my.nordaccount.com/dashboard/nordvpn/access-tokens/")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("NordVPN token check failed: HTTP %d", resp.StatusCode)
	}
	return nil
}

// Countries returns country names that have NordVPN servers, sorted alphabetically.
func Countries(ctx context.Context) ([]string, error) {
	hc := &http.Client{Timeout: nordAPITimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nordAPIBase+"/servers/countries", nil)
	if err != nil {
		return nil, fmt.Errorf("build NordVPN request: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch countries: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	var raw []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse countries: %w", err)
	}

	names := make([]string, 0, len(raw))
	for _, c := range raw {
		if c.Name != "" {
			names = append(names, c.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// nordAPIBase is overridable so tests can point the NordLynx fetch at a stub
// server. Production code never changes it.
var nordAPIBase = nordAPI

// NordLynxConfig fetches a ready-to-use WireGuard configuration for a NordVPN
// account, so callers do not have to export one from the Nord dashboard by
// hand.
//
// NordLynx is WireGuard, which is what makes this possible: the two halves of a
// wg0.conf come from two different places and are assembled here.
//
//   - The private key is the account's NordLynx key, behind an authenticated
//     endpoint. NordVPN's access tokens are presented as HTTP Basic auth with
//     the literal username "token" and the access token as the password.
//   - The server's public key, hostname and port come from the public
//     recommendations endpoint, filtered to servers that actually speak
//     WireGuard. Recommendations are load-aware, so this picks a server Nord
//     considers healthy rather than a fixed one.
//
// country is optional; empty lets Nord recommend from anywhere. An unknown
// country name is an error rather than a silent fallback, because silently
// connecting through a different jurisdiction than the one asked for is the
// kind of surprise this whole template exists to avoid.
//
// Note that this endpoint is not part of a documented, versioned public API.
// It is the same one NordVPN's own clients use, and it can change without
// notice — hence the specific error messages below, which are meant to make a
// break obvious rather than surface as a malformed config.
func NordLynxConfig(ctx context.Context, token, country string) (*WireGuardConfig, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("NordVPN access token is empty")
	}

	privateKey, err := nordLynxPrivateKey(ctx, token)
	if err != nil {
		return nil, err
	}

	server, err := nordRecommendedWireGuardServer(ctx, country)
	if err != nil {
		return nil, err
	}

	return &WireGuardConfig{
		PrivateKey: privateKey,
		// NordLynx assigns every peer the same fixed tunnel address; it is not
		// per-account, and the server does not hand one out dynamically.
		Address: "10.5.0.2/32",
		// Nord's own DNS, inside the tunnel. Leaving this empty would let the
		// guest keep using the LAN resolver, which leaks lookups around the VPN
		// even while the traffic itself is tunnelled.
		DNS:       "103.86.96.100",
		Endpoint:  fmt.Sprintf("%s:51820", server.Station),
		PublicKey: server.PublicKey,
	}, nil
}

// nordLynxPrivateKey returns the account's NordLynx private key.
func nordLynxPrivateKey(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nordAPIBase+"/users/services/credentials", nil)
	if err != nil {
		return "", fmt.Errorf("build NordVPN credentials request: %w", err)
	}
	// Basic auth with the literal username "token"; the access token is the
	// password. Verified against the live API: a malformed header returns
	// "Invalid authorization header" while a well-formed one with bad
	// credentials returns "Unauthorized", so the two failures are separable.
	req.SetBasicAuth("token", token)

	hc := &http.Client{Timeout: nordAPITimeout}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("NordVPN API unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("NordVPN rejected the access token; generate a new one at " +
			"my.nordaccount.com/dashboard/nordvpn/access-tokens/")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("NordVPN credentials request failed: HTTP %d", resp.StatusCode)
	}

	var creds struct {
		NordLynxPrivateKey string `json:"nordlynx_private_key"`
	}
	if err := json.Unmarshal(body, &creds); err != nil {
		return "", fmt.Errorf("parse NordVPN credentials: %w", err)
	}
	if creds.NordLynxPrivateKey == "" {
		return "", fmt.Errorf("NordVPN returned no NordLynx key for this account " +
			"(NordLynx may not be enabled on it, or the API response shape has changed)")
	}
	return creds.NordLynxPrivateKey, nil
}

// nordWireGuardServer is a resolved NordLynx endpoint.
type nordWireGuardServer struct {
	Hostname  string
	Station   string // the server's IP address
	PublicKey string
}

// nordRecommendedWireGuardServer picks a WireGuard-capable server, optionally
// restricted to a country.
func nordRecommendedWireGuardServer(ctx context.Context, country string) (*nordWireGuardServer, error) {
	url := nordAPIBase + "/servers/recommendations?filters[servers_technologies][identifier]=wireguard_udp&limit=1"
	if country != "" {
		id, err := nordCountryID(ctx, country)
		if err != nil {
			return nil, err
		}
		url = fmt.Sprintf("%s&filters[country_id]=%d", url, id)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build NordVPN recommendations request: %w", err)
	}
	hc := &http.Client{Timeout: nordAPITimeout}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch NordVPN server recommendation: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	var servers []struct {
		Hostname     string `json:"hostname"`
		Station      string `json:"station"`
		Technologies []struct {
			Identifier string `json:"identifier"`
			Metadata   []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"metadata"`
		} `json:"technologies"`
	}
	if err := json.Unmarshal(body, &servers); err != nil {
		return nil, fmt.Errorf("parse NordVPN recommendations: %w", err)
	}
	if len(servers) == 0 {
		if country != "" {
			return nil, fmt.Errorf("NordVPN has no WireGuard servers available in %q", country)
		}
		return nil, fmt.Errorf("NordVPN returned no WireGuard servers")
	}

	s := servers[0]
	for _, t := range s.Technologies {
		if t.Identifier != "wireguard_udp" {
			continue
		}
		for _, m := range t.Metadata {
			if m.Name == "public_key" && m.Value != "" {
				return &nordWireGuardServer{Hostname: s.Hostname, Station: s.Station, PublicKey: m.Value}, nil
			}
		}
	}
	return nil, fmt.Errorf("NordVPN server %q advertises no WireGuard public key", s.Hostname)
}

// nordCountryID maps a country name to Nord's numeric ID, case-insensitively.
func nordCountryID(ctx context.Context, country string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nordAPIBase+"/servers/countries", nil)
	if err != nil {
		return 0, fmt.Errorf("build NordVPN countries request: %w", err)
	}
	hc := &http.Client{Timeout: nordAPITimeout}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetch NordVPN countries: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	var raw []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, fmt.Errorf("parse NordVPN countries: %w", err)
	}
	for _, c := range raw {
		if strings.EqualFold(c.Name, country) {
			return c.ID, nil
		}
	}
	return 0, fmt.Errorf("NordVPN has no servers in a country named %q", country)
}
