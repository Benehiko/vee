package vm

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"
)

// startGuestProxy is the shared engine behind SSH proxies and background
// tunnels; verify it honours an explicit bind address and forwards bytes.
func TestStartGuestProxyForwards(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello from guest")
	}))
	defer upstream.Close()

	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	if err != nil {
		t.Fatalf("parse upstream addr: %v", err)
	}
	var upstreamPort int
	if _, err := fmtSscan(portStr, &upstreamPort); err != nil {
		t.Fatalf("parse upstream port: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resolve := func(context.Context) (string, error) { return "127.0.0.1", nil }
	p, err := startGuestProxy(ctx, "testvm", BindLoopback, 0, upstreamPort, resolve, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("startGuestProxy: %v", err)
	}
	defer p.Close()

	if p.BindAddr() != BindLoopback {
		t.Fatalf("BindAddr() = %q, want %q", p.BindAddr(), BindLoopback)
	}
	if p.Port() == 0 {
		t.Fatal("proxy reported port 0")
	}

	resp, err := http.Get("http://127.0.0.1:" + itoa(p.Port()) + "/") //nolint:noctx // short-lived test request
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from guest" {
		t.Fatalf("proxied body = %q", body)
	}
}

// An empty bind address must default to loopback rather than 0.0.0.0, so a
// spec missing the field never silently exposes a guest to the LAN.
func TestStartGuestProxyEmptyBindDefaultsLoopback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resolve := func(context.Context) (string, error) { return "127.0.0.1", nil }
	p, err := startGuestProxy(ctx, "testvm", "", 0, 9, resolve, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("startGuestProxy: %v", err)
	}
	defer p.Close()
	if p.BindAddr() != BindLoopback {
		t.Fatalf("BindAddr() = %q, want %q", p.BindAddr(), BindLoopback)
	}
}

// The router must resolve by Host header and proxy to the matching tunnel,
// preserving the client's Host so guest apps generate links under the
// published name.
func TestTunnelRouterRoutesByHost(t *testing.T) {
	var gotHost, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotPath = r.Host, r.URL.Path
		_, _ = io.WriteString(w, "jellyfin ok")
	}))
	defer upstream.Close()

	var upstreamPort int
	_, portStr, _ := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	if _, err := fmtSscan(portStr, &upstreamPort); err != nil {
		t.Fatalf("parse upstream port: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resolve := func(context.Context) (string, error) { return "127.0.0.1", nil }
	proxy, err := startGuestProxy(ctx, "media", BindLoopback, 0, upstreamPort, resolve, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("startGuestProxy: %v", err)
	}
	defer proxy.Close()

	m := &Manager{}
	m.tunnels.active = map[string]*activeTunnel{
		"media/jellyfin": {
			spec:      TunnelSpec{VM: "media", Service: "jellyfin", Bind: BindHost},
			proxy:     proxy,
			guestPort: 8096,
			protocol:  ServiceHTTP,
		},
	}

	handler := m.tunnelRouterHandler(zap.NewNop())

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/web/index.html", nil)
	req.Host = "jellyfin.benehiko-desktop"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "jellyfin ok" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	// Path is preserved untouched — this is the whole reason vhosts were
	// chosen over path prefixes.
	if gotPath != "/web/index.html" {
		t.Fatalf("upstream path = %q, want the original path unmodified", gotPath)
	}
	if gotHost != "jellyfin.benehiko-desktop" {
		t.Fatalf("upstream Host = %q, want the client's Host preserved", gotHost)
	}
}

// An unmatched hostname gets the index of published names rather than a bare
// 404, so a user who mistypes sees what is actually available.
func TestTunnelRouterUnknownHostServesIndex(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resolve := func(context.Context) (string, error) { return "127.0.0.1", nil }
	proxy, err := startGuestProxy(ctx, "media", BindLoopback, 0, 9, resolve, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("startGuestProxy: %v", err)
	}
	defer proxy.Close()

	m := &Manager{}
	m.tunnels.active = map[string]*activeTunnel{
		"media/jellyfin": {
			spec:     TunnelSpec{VM: "media", Service: "jellyfin"},
			proxy:    proxy,
			protocol: ServiceHTTP,
		},
	}

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	req.Host = "nosuchservice.benehiko-desktop"
	rec := httptest.NewRecorder()
	m.tunnelRouterHandler(zap.NewNop()).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown name", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "jellyfin") {
		t.Fatalf("index should list published names, got: %s", rec.Body.String())
	}
}

// Only HTTP(S) tunnels are routable: a SPICE or raw TCP tunnel has no HTTP
// representation and must not appear in the routing table.
func TestActiveRoutesSkipsNonHTTP(t *testing.T) {
	m := &Manager{}
	m.tunnels.active = map[string]*activeTunnel{
		"media/jellyfin": {
			spec: TunnelSpec{VM: "media", Service: "jellyfin"}, protocol: ServiceHTTP,
		},
		"win/spice": {
			spec: TunnelSpec{VM: "win", Service: "spice"}, protocol: ServiceSPICE,
		},
		"db/postgres": {
			spec: TunnelSpec{VM: "db", Service: "postgres"}, protocol: ServiceTCP,
		},
	}
	routes := m.activeRoutes()
	if len(routes) != 1 {
		t.Fatalf("expected 1 routable tunnel, got %d: %v", len(routes), routes)
	}
	if _, ok := routes["jellyfin"]; !ok {
		t.Fatalf("expected jellyfin to be routable, got %v", routes)
	}
}

// Two VMs exporting the same service name must resolve deterministically
// rather than depending on map iteration order.
func TestActiveRoutesDuplicateLabelIsStable(t *testing.T) {
	m := &Manager{}
	m.tunnels.active = map[string]*activeTunnel{
		"alpha/web": {spec: TunnelSpec{VM: "alpha", Service: "web"}, protocol: ServiceHTTP},
		"beta/web":  {spec: TunnelSpec{VM: "beta", Service: "web"}, protocol: ServiceHTTP},
	}
	first := m.activeRoutes()["web"].spec.VM
	for range 20 {
		if got := m.activeRoutes()["web"].spec.VM; got != first {
			t.Fatalf("duplicate label resolved unstably: %q then %q", first, got)
		}
	}
}

// fmtSscan and itoa keep the test's import list to what it actually exercises.
func fmtSscan(s string, v *int) (int, error) { return fmt.Sscanf(s, "%d", v) }

func itoa(i int) string { return strconv.Itoa(i) }

// TestGuestProxyFallsBackWhenGuestAddressUnreachable covers the case that
// broke background tunnels to qBittorrent and bitmagnet: the service does not
// answer at the guest's own address, either because it is bound to guest
// loopback or because the guest is on user-mode NAT. The proxy must not hang
// on the refused dial — it must reach the service through the fallback.
func TestGuestProxyFallsBackWhenGuestAddressUnreachable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "reached via fallback")
	}))
	defer upstream.Close()

	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(upstream.URL, "http://"))
	if err != nil {
		t.Fatalf("parse upstream addr: %v", err)
	}
	var upstreamPort int
	if _, err := fmtSscan(portStr, &upstreamPort); err != nil {
		t.Fatalf("parse upstream port: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 192.0.2.1 is TEST-NET-1 (RFC 5737): guaranteed not to answer, standing
	// in for a guest address the service is not bound to.
	resolve := func(context.Context) (string, error) { return "192.0.2.1", nil }

	// Counted atomically: the fallback runs on the proxy's forward goroutine,
	// which can outlive the HTTP exchange the test goroutine observes.
	var fallbackCalls atomic.Int64
	fallback := func(ctx context.Context, _ string, targetPort int) (net.Conn, error) {
		fallbackCalls.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", itoa(targetPort)))
	}

	p, err := startGuestProxy(ctx, "torrents", BindLoopback, 0, upstreamPort, resolve, fallback, zap.NewNop())
	if err != nil {
		t.Fatalf("startGuestProxy: %v", err)
	}
	defer p.Close()

	resp, err := http.Get("http://127.0.0.1:" + itoa(p.Port()) + "/") //nolint:noctx // short-lived test request
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "reached via fallback" {
		t.Fatalf("proxied body = %q, want the fallback upstream", body)
	}
	if fallbackCalls.Load() == 0 {
		t.Fatal("fallback was never used; the proxy only tried the guest address")
	}
}
