package utils

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/schollz/progressbar/v3"
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

// DownloadToFile streams url to dst (via DirectHTTPClient, so redirects are
// followed and no dev proxy is used), showing a byte progress bar. It writes to
// a temporary file first and renames on success so a partial download never
// leaves a truncated file at dst that a later run would treat as complete.
func DownloadToFile(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := DirectHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	tmp, err := os.CreateTemp(filepathDir(dst), ".download-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	bar := progressbar.DefaultBytes(resp.ContentLength, "downloading")
	if _, err := io.Copy(io.MultiWriter(tmp, bar), resp.Body); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

// filepathDir is a tiny wrapper kept local to avoid pulling path/filepath into
// callers that only need DownloadToFile's temp-in-same-dir behavior.
func filepathDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}
