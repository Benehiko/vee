package vm

import (
	"path/filepath"
	"testing"
)

func testRegistry(t *testing.T) *TunnelRegistry {
	t.Helper()
	return &TunnelRegistry{path: filepath.Join(t.TempDir(), "tunnels.json")}
}

func TestTunnelRegistryEmpty(t *testing.T) {
	r := testRegistry(t)
	specs, err := r.List()
	if err != nil {
		t.Fatalf("List on a missing registry: %v", err)
	}
	if len(specs) != 0 {
		t.Fatalf("expected no specs, got %d", len(specs))
	}
}

func TestTunnelRegistryAddListRemove(t *testing.T) {
	r := testRegistry(t)

	if err := r.Add(TunnelSpec{VM: "media", Service: "jellyfin", Bind: BindHost, Port: 2301}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := r.Add(TunnelSpec{VM: "torrents", Service: "qbittorrent", Port: 2302}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	specs, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("expected 2 specs, got %d", len(specs))
	}
	// Sorted by VM: media before torrents.
	if specs[0].VM != "media" || specs[1].VM != "torrents" {
		t.Fatalf("specs not sorted by VM: %+v", specs)
	}

	removed, err := r.Remove("media", "jellyfin")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !removed {
		t.Fatal("Remove reported nothing removed for a registered tunnel")
	}
	removed, err = r.Remove("media", "jellyfin")
	if err != nil {
		t.Fatalf("Remove (second): %v", err)
	}
	if removed {
		t.Fatal("Remove reported a removal for an already-removed tunnel")
	}

	specs, _ = r.List()
	if len(specs) != 1 || specs[0].VM != "torrents" {
		t.Fatalf("expected only the torrents tunnel to remain, got %+v", specs)
	}
}

// Re-registering the same VM/service must update in place rather than append,
// otherwise repeated `--background` invocations accumulate duplicate entries
// and the daemon races two listeners for one service.
func TestTunnelRegistryAddReplacesSameKey(t *testing.T) {
	r := testRegistry(t)
	if err := r.Add(TunnelSpec{VM: "media", Service: "jellyfin", Bind: BindLoopback, Port: 2301}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := r.Add(TunnelSpec{VM: "media", Service: "jellyfin", Bind: BindHost, Port: 2305}); err != nil {
		t.Fatalf("Add (replace): %v", err)
	}
	specs, _ := r.List()
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec after replace, got %d: %+v", len(specs), specs)
	}
	if specs[0].Bind != BindHost || specs[0].Port != 2305 {
		t.Fatalf("replace did not take effect: %+v", specs[0])
	}
}

func TestTunnelRegistryRemoveVM(t *testing.T) {
	r := testRegistry(t)
	for _, s := range []TunnelSpec{
		{VM: "media", Service: "jellyfin", Port: 2301},
		{VM: "media", Service: "sonarr", Port: 2302},
		{VM: "torrents", Service: "qbittorrent", Port: 2303},
	} {
		if err := r.Add(s); err != nil {
			t.Fatalf("Add %s: %v", s.Key(), err)
		}
	}
	if err := r.RemoveVM("media"); err != nil {
		t.Fatalf("RemoveVM: %v", err)
	}
	specs, _ := r.List()
	if len(specs) != 1 || specs[0].VM != "torrents" {
		t.Fatalf("expected only torrents to survive, got %+v", specs)
	}
	// Removing a VM with no tunnels is a no-op, not an error.
	if err := r.RemoveVM("nonexistent"); err != nil {
		t.Fatalf("RemoveVM on an unknown VM: %v", err)
	}
}

func TestTunnelSpecDefaults(t *testing.T) {
	// A spec written without Bind (or hand-edited) must default to loopback:
	// defaulting to 0.0.0.0 would silently publish a service to the LAN.
	s := TunnelSpec{VM: "media", Service: "jellyfin"}
	if got := s.BindAddr(); got != BindLoopback {
		t.Fatalf("BindAddr() = %q, want %q", got, BindLoopback)
	}
	if got := s.VHost(); got != "jellyfin" {
		t.Fatalf("VHost() = %q, want the service name", got)
	}
	s.Hostname = "media-server"
	if got := s.VHost(); got != "media-server" {
		t.Fatalf("VHost() = %q, want the override", got)
	}
	if got := (TunnelSpec{Bind: "192.168.1.5"}).BindAddr(); got != BindLoopback {
		t.Fatalf("an unrecognized bind must fall back to loopback, got %q", got)
	}
}

func TestSanitizeHostname(t *testing.T) {
	cases := map[string]string{
		"jellyfin":          "jellyfin",
		"Home Assistant":    "home-assistant",
		"qBittorrent":       "qbittorrent",
		"my_service.v2":     "my-service-v2",
		"--leading-trail--": "leading-trail",
		"!!!":               "",
		"":                  "",
	}
	for in, want := range cases {
		if got := SanitizeHostname(in); got != want {
			t.Errorf("SanitizeHostname(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRouterLabel(t *testing.T) {
	cases := map[string]string{
		"jellyfin.benehiko-desktop":    "jellyfin",
		"jellyfin.benehiko-desktop:80": "jellyfin",
		"Jellyfin.Benehiko-Desktop":    "jellyfin",
		"benehiko-desktop":             "benehiko-desktop",
		"":                             "",
	}
	for in, want := range cases {
		if got := routerLabel(in); got != want {
			t.Errorf("routerLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
