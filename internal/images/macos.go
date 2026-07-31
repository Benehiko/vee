package images

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Benehiko/vee/internal/vzhelper"
	"github.com/Benehiko/vee/provider"
)

// DistroMacOS pulls macOS restore images (IPSW) for the vz backend. There is
// no version list to pin: "latest" asks the host's Virtualization.framework
// (via vee-vz-helper) for the newest restore image THIS host can restore,
// and any https URL (e.g. from ipsw.me / AppleDB for older versions) is
// accepted verbatim.
const DistroMacOS = "macos"

// MacOSImage caches a macOS IPSW in the vee image cache. IPSWs are ~14+ GB.
type MacOSImage struct {
	*BaseImage
	url     string
	version string
}

// NewMacOSImage builds the image for a version spec: "latest"/"" resolves
// through the helper (deferred to Download, which carries the context); an
// https URL is validated eagerly.
func NewMacOSImage(p provider.Provider, version string) (*MacOSImage, error) {
	img := &MacOSImage{BaseImage: NewBaseImage(p), version: "latest"}
	if version == "" || version == "latest" {
		return img, nil // URL resolved lazily by Download
	}
	if err := img.setURL(version); err != nil {
		return nil, err
	}
	return img, nil
}

// setURL validates and adopts an explicit IPSW URL.
func (m *MacOSImage) setURL(ipswURL string) error {
	if !strings.HasPrefix(ipswURL, "https://") {
		return fmt.Errorf("macos version must be \"latest\" or an https IPSW URL (got %q) — find older versions at https://ipsw.me or https://appledb.dev", ipswURL)
	}
	parsed, err := url.Parse(ipswURL)
	if err != nil {
		return fmt.Errorf("parse IPSW URL: %w", err)
	}
	base := path.Base(parsed.Path)
	if base == "" || base == "/" || base == "." || !strings.HasSuffix(base, ".ipsw") {
		return fmt.Errorf("IPSW URL must end in a .ipsw filename (got %q)", ipswURL)
	}
	m.url = ipswURL
	m.version = macOSVersionFromFilename(base)
	return nil
}

// resolve fills in the URL for "latest" via the host's
// Virtualization.framework (the helper binary). When the network lookup
// fails but a restore image is already cached, the newest cached one is
// used — a cached multi-gigabyte IPSW must stay usable offline.
func (m *MacOSImage) resolve(ctx context.Context) error {
	if m.url != "" {
		return nil
	}
	helperPath, err := vzhelper.ResolveHelper()
	if err != nil {
		return err
	}
	ipswURL, err := vzhelper.LatestRestoreImageURL(ctx, helperPath)
	if err != nil {
		if cached := m.newestCached(); cached != "" {
			// Stderr, not stdout: `vee mcp` owns stdout for the MCP
			// protocol stream, and this is a warning anyway.
			fmt.Fprintf(os.Stderr, "Could not query the latest restore image (%v); using cached %s\n", err, cached)
			return m.setURL("https://cached.invalid/" + cached)
		}
		return err
	}
	return m.setURL(ipswURL)
}

// newestCached returns the lexically newest cached UniversalMac IPSW
// filename, or "".
func (m *MacOSImage) newestCached() string {
	matches, err := filepath.Glob(filepath.Join(m.BaseImage.AbsolutePath(), "UniversalMac_*_Restore.ipsw"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	newest := ""
	for _, p := range matches {
		if base := filepath.Base(p); base > newest {
			newest = base
		}
	}
	return newest
}

// macOSVersionFromFilename extracts "15.5" from
// "UniversalMac_15.5_24F74_Restore.ipsw"; falls back to the filename.
func macOSVersionFromFilename(base string) string {
	parts := strings.Split(base, "_")
	if len(parts) >= 2 && strings.HasPrefix(parts[0], "UniversalMac") {
		return parts[1]
	}
	return strings.TrimSuffix(base, ".ipsw")
}

// Name is the cache file name; empty until a "latest" spec is resolved by
// Download.
func (m *MacOSImage) Name() string {
	if m.url == "" {
		return ""
	}
	parsed, _ := url.Parse(m.url)
	return path.Base(parsed.Path)
}

func (m *MacOSImage) AbsolutePath() string {
	return filepath.Join(m.BaseImage.AbsolutePath(), m.Name())
}

func (m *MacOSImage) Distro() string  { return DistroMacOS }
func (m *MacOSImage) Version() string { return m.version }

func (m *MacOSImage) Delete() error {
	// An unresolved "latest" has no filename; AbsolutePath would be the
	// bare cache directory — never remove that.
	if m.Name() == "" {
		return fmt.Errorf("nothing cached: the restore image URL was never resolved")
	}
	return os.Remove(m.AbsolutePath())
}

// Download streams the IPSW into the cache. Apple's CDN serves these
// anonymously; the file is version-stamped so the cache is a no-op on hits.
func (m *MacOSImage) Download(ctx context.Context) error {
	if err := m.resolve(ctx); err != nil {
		return err
	}
	dst := m.AbsolutePath()
	if _, err := os.Stat(dst); err == nil {
		return nil // cached
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %s", m.url, resp.Status)
	}

	tmp := dst + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // cache path derived from vee config
	if err != nil {
		return err
	}
	if err := m.CreateImage(f, resp.Body, resp.ContentLength); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
