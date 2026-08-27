package vm

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func testLogger() *zap.Logger { return zap.NewNop() }

// upstreamPort spins up an HTTP server standing in for a guest service and
// returns its port plus the body it serves.
func upstreamPort(t *testing.T, body string) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("split upstream addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse upstream port: %v", err)
	}
	return port
}

// TunnelStatuses must report a registered tunnel even when no listener is up,
// so `--list` can show what will be restored once the VM comes back rather
// than hiding it.
func TestTunnelStatusesReportsInactiveRegistration(t *testing.T) {
	m := &Manager{tunnelRegistryPath: t.TempDir() + "/tunnels.json"}
	if err := m.Tunnels().Add(TunnelSpec{
		VM: "media", Service: "jellyfin", Bind: BindHost, Port: 2301, Protocol: ServiceHTTP,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	m.tunnels.inactive = map[string]string{"media/jellyfin": "VM not running"}

	statuses, err := m.TunnelStatuses()
	if err != nil {
		t.Fatalf("TunnelStatuses: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	st := statuses[0]
	if st.Active {
		t.Fatal("a tunnel with no listener must not report active")
	}
	if st.Reason != "VM not running" {
		t.Fatalf("Reason = %q, want the reconcile reason", st.Reason)
	}
	if st.Bind != BindHost || st.Port != 2301 || st.Protocol != ServiceHTTP {
		t.Fatalf("registration details lost: %+v", st)
	}
	if st.Hostname != "jellyfin" {
		t.Fatalf("Hostname = %q, want the service name default", st.Hostname)
	}
}

// An active tunnel must report the port and bind address actually bound,
// which can differ from the registered ones when the recorded port was taken.
func TestTunnelStatusesReportsActiveListener(t *testing.T) {
	port := upstreamPort(t, "ok")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := &Manager{tunnelRegistryPath: t.TempDir() + "/tunnels.json"}
	spec := TunnelSpec{VM: "media", Service: "jellyfin", Bind: BindLoopback, Protocol: ServiceHTTP}
	if err := m.Tunnels().Add(spec); err != nil {
		t.Fatalf("Add: %v", err)
	}

	resolve := func(context.Context) (string, error) { return "127.0.0.1", nil }
	proxy, err := startGuestProxy(ctx, "media", BindLoopback, 0, port, resolve, nil, testLogger())
	if err != nil {
		t.Fatalf("startGuestProxy: %v", err)
	}
	defer proxy.Close()

	m.tunnels.active = map[string]*activeTunnel{
		spec.Key(): {spec: spec, proxy: proxy, guestPort: port, protocol: ServiceHTTP},
	}

	statuses, err := m.TunnelStatuses()
	if err != nil {
		t.Fatalf("TunnelStatuses: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	st := statuses[0]
	if !st.Active {
		t.Fatalf("expected active, got %+v", st)
	}
	// The registered port was 0 (unallocated); the reported one must be the
	// port the listener actually bound, or the URL printed is useless.
	if st.Port != proxy.Port() || st.Port == 0 {
		t.Fatalf("Port = %d, want the bound port %d", st.Port, proxy.Port())
	}
	if st.GuestPort != port {
		t.Fatalf("GuestPort = %d, want %d", st.GuestPort, port)
	}
	if st.Reason != "" {
		t.Fatalf("an active tunnel must carry no reason, got %q", st.Reason)
	}
}

// The proxy resolves the guest address per connection rather than once at
// start, so a guest that changes address (a DHCP renewal, or a guest that had
// not booted when the tunnel came up) keeps working without a restart. Two
// listeners on distinct loopback addresses stand in for the guest moving.
func TestGuestProxyReresolvesPerConnection(t *testing.T) {
	// Bind the first server on an ephemeral port, then bind the second on the
	// SAME port but a different loopback address. Sharing the port makes the
	// resolver's answer the only thing that decides which server is reached.
	var lc net.ListenConfig
	lnCtx := t.Context()

	firstLn, err := lc.Listen(lnCtx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on 127.0.0.1: %v", err)
	}
	port := firstLn.Addr().(*net.TCPAddr).Port

	secondLn, err := lc.Listen(lnCtx, "tcp", "127.0.0.2:"+strconv.Itoa(port))
	if err != nil {
		_ = firstLn.Close()
		t.Skipf("127.0.0.2:%d unavailable (no loopback alias on this host): %v", port, err)
	}

	serve(t, firstLn, "first")
	serve(t, secondLn, "second")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The resolver is called from the proxy's own goroutine, so the address
	// the test swaps mid-run has to be published atomically.
	var current atomic.Pointer[string]
	first := "127.0.0.1"
	current.Store(&first)
	resolve := func(context.Context) (string, error) { return *current.Load(), nil }

	p, err := startGuestProxy(ctx, "media", BindLoopback, 0, port, resolve, nil, testLogger())
	if err != nil {
		t.Fatalf("startGuestProxy: %v", err)
	}
	defer p.Close()

	if got := getBody(t, p.Port()); got != "first" {
		t.Fatalf("body = %q, want %q", got, "first")
	}

	// The guest "moves" to a new address. The listener is untouched: only a
	// per-connection resolve can pick this up.
	second := "127.0.0.2"
	current.Store(&second)
	if got := getBody(t, p.Port()); got != "second" {
		t.Fatalf("after the guest address changed, body = %q, want %q", got, "second")
	}
}

// serve runs an HTTP server on ln returning a fixed body, stopped at cleanup.
func serve(t *testing.T, ln net.Listener, body string) {
	t.Helper()
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// getBody issues a GET through the proxy on a fresh connection. Connection
// reuse is disabled deliberately: the proxy resolves the guest address once
// per *connection*, so a pooled connection would keep hitting the old address
// and hide whether re-resolution works at all.
func getBody(t *testing.T, port int) string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://127.0.0.1:"+strconv.Itoa(port)+"/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
