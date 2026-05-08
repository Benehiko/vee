package vpn

import (
	"fmt"
	"io"
	"net/http"
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
func ValidateToken(token string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("NordVPN access token is empty")
	}
	hc := &http.Client{Timeout: nordAPITimeout}
	resp, err := hc.Get(nordAPI + "/servers/countries")
	if err != nil {
		return fmt.Errorf("NordVPN API unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
