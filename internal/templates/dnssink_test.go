package templates

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestAdguardHomeConfigIsValidYAML guards the hand-built AdGuardHome.yaml: an
// unparseable config makes AdGuard Home fall back to the setup wizard, so the
// VM would boot resolving nothing.
func TestAdguardHomeConfigIsValidYAML(t *testing.T) {
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(adguardHomeConfig("admin", "$2a$10$abcdefghijklmnopqrstuv")), &parsed); err != nil {
		t.Fatalf("adguardHomeConfig is not valid YAML: %v", err)
	}

	dns, ok := parsed["dns"].(map[string]any)
	if !ok {
		t.Fatal("config has no dns section")
	}
	if port, _ := dns["port"].(int); port != DNSSinkDNSPort {
		t.Errorf("dns.port = %v, want %d", dns["port"], DNSSinkDNSPort)
	}
	// Blocking is the whole point of the template; both switches must be on
	// before the first query arrives.
	if enabled, _ := dns["filtering_enabled"].(bool); !enabled {
		t.Error("dns.filtering_enabled is false; the sinkhole would resolve ads")
	}
	if enabled, _ := dns["protection_enabled"].(bool); !enabled {
		t.Error("dns.protection_enabled is false; the sinkhole would resolve ads")
	}

	filters, ok := parsed["filters"].([]any)
	if !ok || len(filters) == 0 {
		t.Fatal("config declares no blocklists")
	}
}

// TestAdguardHomeConfigAuth covers both admin-credential shapes: a bcrypt hash
// produces a login, and an empty hash leaves the users list empty rather than
// emitting a half-written entry that AdGuard Home would reject.
func TestAdguardHomeConfigAuth(t *testing.T) {
	withUser := adguardHomeConfig("alano", "$2a$10$abcdefghijklmnopqrstuv")
	if !strings.Contains(withUser, "name: alano") {
		t.Error("admin username missing from config")
	}
	if !strings.Contains(withUser, "password: $2a$10$abcdefghijklmnopqrstuv") {
		t.Error("admin password hash missing from config")
	}

	noUser := adguardHomeConfig("alano", "")
	if !strings.Contains(noUser, "users: []") {
		t.Error("empty password hash should leave users empty")
	}
	if strings.Contains(noUser, "name: alano") {
		t.Error("empty password hash should not emit a user entry")
	}

	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(noUser), &parsed); err != nil {
		t.Fatalf("unauthenticated config is not valid YAML: %v", err)
	}
}

// TestAdguardOpenRCService checks the init script wires the binary to the
// cloud-init-written config. Alpine has no systemd, so this hand-rolled unit
// is the only thing starting AdGuard Home at boot.
func TestAdguardOpenRCService(t *testing.T) {
	svc := adguardOpenRCService()
	for _, want := range []string{
		"#!/sbin/openrc-run",
		"/usr/local/bin/AdGuardHome",
		"--config /opt/AdGuardHome/AdGuardHome.yaml",
		"command_background=\"yes\"",
		"need net",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("OpenRC service missing %q", want)
		}
	}
}
