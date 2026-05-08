package vpn

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
)

const (
	nordAPI        = "https://api.nordvpn.com/v1"
	nordAPITimeout = 15 * time.Second
)

// NordVPNClient handles WireGuard config generation via the NordVPN API.
type NordVPNClient struct {
	http  *http.Client
	token string
}

// NordVPNServer is a WireGuard-capable NordVPN server.
type NordVPNServer struct {
	Hostname  string
	IP        string
	PublicKey string
	Country   string
	Load      int
}

// NordVPNCountry is a country available in the NordVPN network.
type NordVPNCountry struct {
	ID   int
	Name string
	Code string
}

// WireGuardConfig holds the data needed to render a wg0.conf.
type WireGuardConfig struct {
	PrivateKey string
	Address    string // assigned IP, e.g. 10.5.0.2/32
	DNS        string
	Endpoint   string // host:port
	PublicKey  string // server pubkey
}

// NewNordVPNClient authenticates with username+password and returns a client.
func NewNordVPNClient(username, password string) (*NordVPNClient, error) {
	hc := &http.Client{Timeout: nordAPITimeout}

	req, err := http.NewRequest("POST", nordAPI+"/users/tokens", bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(username, password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("auth failed (status %d): %s", resp.StatusCode, body)
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Token == "" {
		return nil, fmt.Errorf("parse auth response: %w (body: %s)", err, body)
	}

	return &NordVPNClient{http: hc, token: result.Token}, nil
}

// GenerateWireGuardKeys generates a WireGuard private/public key pair.
func GenerateWireGuardKeys() (privateKey, publicKey string, err error) {
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return "", "", fmt.Errorf("generate private key: %w", err)
	}
	// Clamp per RFC 7748.
	priv[0] &= 248
	priv[31] = (priv[31] & 127) | 64

	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return "", "", fmt.Errorf("derive public key: %w", err)
	}

	return base64.StdEncoding.EncodeToString(priv[:]),
		base64.StdEncoding.EncodeToString(pub), nil
}

// RegisterKey registers a WireGuard public key with NordVPN and returns the
// assigned internal IP address (e.g. "10.5.0.2").
func (c *NordVPNClient) RegisterKey(publicKey string) (assignedIP string, err error) {
	payload, _ := json.Marshal(map[string]string{"public_key": publicKey})
	req, err := http.NewRequest("POST", nordAPI+"/users/keys", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("register key: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("register key failed (status %d): %s", resp.StatusCode, body)
	}

	var result struct {
		IPAddress string `json:"ip_address"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.IPAddress == "" {
		return "", fmt.Errorf("parse key registration response: %w (body: %s)", err, body)
	}
	return result.IPAddress, nil
}

// Countries returns the list of countries that have WireGuard servers.
func Countries() ([]NordVPNCountry, error) {
	hc := &http.Client{Timeout: nordAPITimeout}
	resp, err := hc.Get(nordAPI + "/countries")
	if err != nil {
		return nil, fmt.Errorf("fetch countries: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var raw []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse countries: %w", err)
	}

	out := make([]NordVPNCountry, len(raw))
	for i, c := range raw {
		out[i] = NordVPNCountry{ID: c.ID, Name: c.Name, Code: c.Code}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// RecommendedServer returns the lowest-load WireGuard server for countryID
// (pass 0 to get the globally recommended server).
func RecommendedServer(countryID int) (*NordVPNServer, error) {
	hc := &http.Client{Timeout: nordAPITimeout}

	url := nordAPI + "/servers/recommendations?filters[servers_technologies][identifier]=wireguard_udp&limit=1"
	if countryID > 0 {
		url += fmt.Sprintf("&filters[country_id]=%d", countryID)
	}

	resp, err := hc.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch server: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var servers []struct {
		Hostname   string `json:"hostname"`
		Station    string `json:"station"`
		Load       int    `json:"load"`
		Locations  []struct {
			Country struct {
				Name string `json:"name"`
			} `json:"country"`
		} `json:"locations"`
		Technologies []struct {
			Identifier string `json:"identifier"`
			Metadata   []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"metadata"`
		} `json:"technologies"`
	}
	if err := json.Unmarshal(body, &servers); err != nil || len(servers) == 0 {
		return nil, fmt.Errorf("no WireGuard server found (country_id=%d): %w", countryID, err)
	}

	s := servers[0]
	var pubKey, country string
	for _, t := range s.Technologies {
		if t.Identifier == "wireguard_udp" {
			for _, m := range t.Metadata {
				if m.Name == "public_key" {
					pubKey = m.Value
				}
			}
		}
	}
	if len(s.Locations) > 0 {
		country = s.Locations[0].Country.Name
	}
	if pubKey == "" {
		return nil, fmt.Errorf("server %s has no WireGuard public key", s.Hostname)
	}

	return &NordVPNServer{
		Hostname:  s.Hostname,
		IP:        s.Station,
		PublicKey: pubKey,
		Country:   country,
		Load:      s.Load,
	}, nil
}

// ParseWireGuardConf parses a minimal wg0.conf into a WireGuardConfig.
// It only extracts the fields vee needs; other directives are preserved in the
// raw content but not modelled.
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

// GenerateConfig creates a complete WireGuard config for the given server
// using the NordVPN API to register the key and get an assigned IP.
func (c *NordVPNClient) GenerateConfig(server *NordVPNServer) (*WireGuardConfig, error) {
	privKey, pubKey, err := GenerateWireGuardKeys()
	if err != nil {
		return nil, err
	}

	assignedIP, err := c.RegisterKey(pubKey)
	if err != nil {
		return nil, err
	}

	return &WireGuardConfig{
		PrivateKey: privKey,
		Address:    assignedIP + "/32",
		DNS:        "103.86.96.100", // NordVPN DNS
		Endpoint:   server.IP + ":51820",
		PublicKey:  server.PublicKey,
	}, nil
}
