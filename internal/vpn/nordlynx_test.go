package vpn

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubNord serves the two endpoints NordLynxConfig depends on, in the shapes
// verified against the live API.
func stubNord(t *testing.T, wantToken string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/users/services/credentials", func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		// The live API takes the literal username "token" with the access
		// token as the password.
		if !ok || user != "token" || pass != wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errors":{"message":"Unauthorized","code":101301}}`))
			return
		}
		_, _ = w.Write([]byte(`{"nordlynx_private_key":"cHJpdmF0ZS1rZXktZm9yLXRlc3RpbmctcHVycG9zZXM="}`))
	})

	mux.HandleFunc("/servers/countries", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":153,"name":"Netherlands"},{"id":81,"name":"Germany"}]`))
	})

	mux.HandleFunc("/servers/recommendations", func(w http.ResponseWriter, r *http.Request) {
		if id := r.URL.Query().Get("filters[country_id]"); id == "999" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"hostname":"nl1254.nordvpn.com","station":"192.0.2.10",
			"technologies":[{"identifier":"openvpn_udp","metadata":[]},
			{"identifier":"wireguard_udp","metadata":[{"name":"public_key","value":"c2VydmVyLXB1YmxpYy1rZXktZm9yLXRlc3Rpbmc="}]}]}]`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	old := nordAPIBase
	nordAPIBase = srv.URL
	t.Cleanup(func() { nordAPIBase = old })
	return srv
}

// TestNordLynxConfigAssemblesBothHalves covers the success path end to end.
// A wg0.conf needs the account's private key and the server's public key, and
// those come from two different endpoints — one authenticated, one not.
func TestNordLynxConfigAssemblesBothHalves(t *testing.T) {
	stubNord(t, "good-token")

	cfg, err := NordLynxConfig(context.Background(), "good-token", "")
	if err != nil {
		t.Fatalf("NordLynxConfig: %v", err)
	}
	if cfg.PrivateKey != "cHJpdmF0ZS1rZXktZm9yLXRlc3RpbmctcHVycG9zZXM=" {
		t.Errorf("private key = %q", cfg.PrivateKey)
	}
	if cfg.PublicKey != "c2VydmVyLXB1YmxpYy1rZXktZm9yLXRlc3Rpbmc=" {
		t.Errorf("server public key = %q", cfg.PublicKey)
	}
	// The endpoint must be the station IP, not the hostname: the guest's
	// kill-switch pins the handshake hole to resolved addresses, and once
	// OUTPUT is DROP there is no DNS left to resolve a hostname with.
	if cfg.Endpoint != "192.0.2.10:51820" {
		t.Errorf("endpoint = %q, want the station IP with the WireGuard port", cfg.Endpoint)
	}
	if cfg.DNS == "" {
		t.Error("no DNS set; the guest would keep using the LAN resolver and leak lookups around the tunnel")
	}
	if cfg.Address == "" {
		t.Error("no tunnel address set; wg-quick would have nothing to assign")
	}

	// The result has to survive the round trip into a real wg0.conf, since
	// that is the only thing the guest ever sees.
	reparsed, err := ParseWireGuardConf(RenderWireGuardConf(cfg))
	if err != nil {
		t.Fatalf("rendered config does not parse back: %v", err)
	}
	if reparsed.PrivateKey != cfg.PrivateKey || reparsed.Endpoint != cfg.Endpoint {
		t.Error("config did not survive the render/parse round trip")
	}
}

// TestNordLynxConfigRejectsBadToken checks the authentication failure is
// reported as a token problem with the URL to fix it, not as a generic HTTP
// error the user cannot act on.
func TestNordLynxConfigRejectsBadToken(t *testing.T) {
	stubNord(t, "good-token")

	_, err := NordLynxConfig(context.Background(), "wrong-token", "")
	if err == nil {
		t.Fatal("bad token accepted")
	}
	if !strings.Contains(err.Error(), "access-tokens") {
		t.Errorf("error does not tell the user how to fix it: %v", err)
	}
}

func TestNordLynxConfigEmptyToken(t *testing.T) {
	if _, err := NordLynxConfig(context.Background(), "   ", ""); err == nil {
		t.Fatal("empty token accepted")
	}
}

// TestNordLynxConfigCountry covers country selection, including the case that
// matters most: an unknown country must fail rather than silently connecting
// through a different jurisdiction than the one asked for.
func TestNordLynxConfigCountry(t *testing.T) {
	stubNord(t, "good-token")

	if _, err := NordLynxConfig(context.Background(), "good-token", "netherlands"); err != nil {
		t.Errorf("case-insensitive country match failed: %v", err)
	}

	_, err := NordLynxConfig(context.Background(), "good-token", "Atlantis")
	if err == nil {
		t.Fatal("unknown country silently accepted; the guest would connect somewhere unintended")
	}
	if !strings.Contains(err.Error(), "Atlantis") {
		t.Errorf("error does not name the rejected country: %v", err)
	}
}

// TestNordLynxPrivateKeyIsUsable guards against a config that renders but
// cannot work: WireGuard keys are base64-encoded 32-byte values, so a key that
// is not decodable would fail inside the guest with no useful diagnostic.
func TestNordLynxPrivateKeyIsUsable(t *testing.T) {
	stubNord(t, "good-token")

	cfg, err := NordLynxConfig(context.Background(), "good-token", "")
	if err != nil {
		t.Fatalf("NordLynxConfig: %v", err)
	}
	for name, key := range map[string]string{"private": cfg.PrivateKey, "public": cfg.PublicKey} {
		if _, err := base64.StdEncoding.DecodeString(key); err != nil {
			t.Errorf("%s key is not valid base64: %v", name, err)
		}
	}
}
