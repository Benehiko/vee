package utils

import (
	"net/http"
	"time"
)

// DirectHTTPClient returns an http.Client that ignores the process's
// HTTP_PROXY / HTTPS_PROXY environment, so vee's outbound fetches never
// go through a developer-side caching MITM proxy (e.g. claude-code's
// float proxy). This is intentional: vee downloads OS install media and
// validates it against an upstream checksum — a transparent caching
// proxy would either break TLS verification (cert chain ends at the
// proxy CA, not the system store) or hand back stale content.
//
// Internal HTTP between vee components and external developer tooling
// can opt back into the env proxy by using http.DefaultClient instead.
func DirectHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return &http.Client{
		Transport: t,
		Timeout:   0,
	}
}

// DirectHTTPClientWithTimeout is like DirectHTTPClient but applies a
// per-request timeout. Use for short metadata fetches (checksums,
// release indexes); pass a long or zero timeout for bulk image
// downloads.
func DirectHTTPClientWithTimeout(d time.Duration) *http.Client {
	c := DirectHTTPClient()
	c.Timeout = d
	return c
}
