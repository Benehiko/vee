package vm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

// tunnelRouterPort is the port the vhost router listens on. Port 80 is what
// makes the published names work without a port suffix in the URL bar, which
// is the whole point of routing by hostname rather than by port.
const tunnelRouterPort = 80

// routerBindAddr is 0.0.0.0 because the router only ever serves tunnels the
// user already opted into publishing with --host; a loopback-only router
// would make those unreachable from the LAN devices they were published for.
const routerBindAddr = BindHost

// serveTunnelRouter runs an HTTP reverse proxy that maps
// <service>.<hostname>/ to the corresponding background tunnel, so a guest's
// web UI is reachable under its own name with its own root path.
//
// Routing by hostname rather than path prefix is deliberate: web UIs emit
// absolute asset URLs (/static/..., /api/...), which a stripped path prefix
// breaks unless every app is separately configured with a base URL. Under a
// vhost the app keeps the root path and needs no configuration at all.
//
// The router is best-effort. Binding port 80 requires CAP_NET_BIND_SERVICE
// (granted by the systemd unit) or root; without it the daemon logs once and
// carries on, and every tunnel is still reachable directly on its own port.
func (m *Manager) serveTunnelRouter(ctx context.Context) {
	log := m.provider.Logger()

	var lc net.ListenConfig
	addr := net.JoinHostPort(routerBindAddr, fmt.Sprintf("%d", tunnelRouterPort))
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		log.Info("tunnel router not started; tunnels remain reachable on their own ports",
			zap.String("addr", addr), zap.Error(err))
		return
	}

	srv := &http.Server{
		Handler:           m.tunnelRouterHandler(log),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: proxied responses include long-lived streams
		// (media playback, server-sent events) that a deadline would sever
		// mid-transfer.
	}
	go func() { //nolint:gosec // G118: parent ctx is already cancelled here; graceful shutdown needs a fresh deadline, not the cancelled ctx.
		<-ctx.Done()
		// ctx is already cancelled; draining in-flight requests needs its
		// own timeout budget from a fresh root context.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx) //nolint:contextcheck // parent ctx is already cancelled; shutdown requires a fresh deadline to drain in-flight requests
	}()

	log.Info("tunnel router listening",
		zap.String("addr", addr), zap.String("suffix", routerHostSuffix()))
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Warn("tunnel router stopped", zap.Error(err))
	}
}

// tunnelRouterHandler resolves the request's Host header to a live tunnel and
// proxies to it, serving an index of published names when the host matches no
// tunnel (including a bare hit on the machine's own hostname).
func (m *Manager) tunnelRouterHandler(log *zap.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes := m.activeRoutes()
		label := routerLabel(r.Host)

		t, ok := routes[label]
		if !ok {
			m.writeRouterIndex(w, routes, label)
			return
		}

		target, err := m.tunnelTargetURL(r.Context(), t)
		if err != nil {
			log.Debug("tunnel router could not resolve target",
				zap.String("vm", t.spec.VM), zap.Error(err))
			http.Error(w, fmt.Sprintf("vee: %s is not reachable right now", label), http.StatusBadGateway)
			return
		}

		proxy := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				// Preserve the client's Host so the guest app generates
				// links under the published name rather than the guest IP.
				pr.Out.Host = pr.In.Host
				pr.SetXForwarded()
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				log.Debug("tunnel router upstream error",
					zap.String("vm", t.spec.VM), zap.Error(err))
				http.Error(w, fmt.Sprintf("vee: %s is not reachable right now", label), http.StatusBadGateway)
			},
		}
		proxy.ServeHTTP(w, r)
	})
}

// tunnelTargetURL builds the upstream URL for a tunnel. It targets the
// tunnel's own listener rather than the guest directly, so the router
// inherits the proxy's per-connection IP resolution instead of duplicating
// it and going stale on a DHCP renewal.
func (m *Manager) tunnelTargetURL(_ context.Context, t *activeTunnel) (*url.URL, error) {
	scheme := "http"
	if t.protocol == ServiceHTTPS {
		scheme = "https"
	}
	// The proxy may be bound to 0.0.0.0; dial it over loopback, which that
	// binding always covers.
	return url.Parse(fmt.Sprintf("%s://127.0.0.1:%d", scheme, t.proxy.Port()))
}

// routerLabel extracts the leading DNS label from a Host header, dropping any
// port and normalizing case, so "Jellyfin.benehiko-desktop:80" resolves the
// same tunnel as "jellyfin.benehiko-desktop".
func routerLabel(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(strings.ToLower(host), ".")
	label, _, _ := strings.Cut(host, ".")
	return SanitizeHostname(label)
}

// routerHostSuffix is the machine's hostname, under which published tunnel
// names live (jellyfin.<suffix>). Falls back to "local" when the hostname is
// unavailable, which still yields usable mDNS-style names.
func routerHostSuffix() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "local"
	}
	// Strip any domain part; the published names append a single label.
	label, _, _ := strings.Cut(h, ".")
	return strings.ToLower(label)
}

// writeRouterIndex serves the list of published tunnel names. This is what a
// user hitting the bare host sees, and it is also the useful answer when a
// name does not resolve to a tunnel — it shows what does.
func (m *Manager) writeRouterIndex(w http.ResponseWriter, routes map[string]*activeTunnel, requested string) {
	labels := make([]string, 0, len(routes))
	for label := range routes {
		labels = append(labels, label)
	}
	sort.Strings(labels)

	status := http.StatusOK
	if requested != "" && len(routes) > 0 {
		status = http.StatusNotFound
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)

	suffix := routerHostSuffix()
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>vee tunnels</title>`)
	b.WriteString(`<style>body{font-family:system-ui,monospace;background:#0d0d0d;color:#e0e0e0;padding:2rem}`)
	b.WriteString(`h1{color:#5af}a{color:#5af}li{margin:.3rem 0}code{color:#888}</style></head><body>`)
	b.WriteString(`<h1>vee tunnels</h1>`)
	if len(routes) == 0 {
		b.WriteString(`<p>No HTTP tunnels are published.</p>`)
		b.WriteString(`<p><code>vee tunnel &lt;vm&gt; &lt;service&gt; --host --background</code></p>`)
	} else {
		b.WriteString(`<ul>`)
		for _, label := range labels {
			t := routes[label]
			host := label + "." + suffix
			fmt.Fprintf(&b, `<li><a href="http://%s/">%s</a> <code>&mdash; %s/%s &rarr; guest:%d</code></li>`,
				htmlEscape(host), htmlEscape(host),
				htmlEscape(t.spec.VM), htmlEscape(t.spec.Service), t.guestPort)
		}
		b.WriteString(`</ul>`)
	}
	b.WriteString(`</body></html>`)
	_, _ = w.Write([]byte(b.String()))
}

// htmlEscape escapes the few characters that matter for the index page. VM
// and service names are vee-controlled, but they are user-supplied strings
// reaching an HTML page, so they are escaped rather than trusted.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}
