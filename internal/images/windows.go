package images

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/utils"
	"github.com/Benehiko/vee/provider"
)

// WindowsVersion identifies a Windows edition for download.
type WindowsVersion string

const (
	Windows11         WindowsVersion = "win11"
	Windows10         WindowsVersion = "win10"
	WindowsServer2025 WindowsVersion = "server2025"
	WindowsServer2022 WindowsVersion = "server2022"

	uupdumpListURL = "https://api.uupdump.net/listid.php?search=%s&lang=%s&edition=%s"
	uupdumpGetURL  = "https://api.uupdump.net/get.php?id=%s&lang=%s&edition=%s&noLinks=0"
	uupdumpLang    = "en-us"
)

// KnownWindowsVersions lists all supported Windows versions, newest first.
var KnownWindowsVersions = []WindowsVersion{
	Windows11,
	Windows10,
	WindowsServer2025,
	WindowsServer2022,
}

type windowsParams struct {
	search  string // search term for UUP dump API
	edition string // edition slug (Core, ServerStandard, etc.)
}

var windowsVersionParams = map[WindowsVersion]windowsParams{
	Windows11:         {search: "Windows 11 24H2", edition: "Core"},
	Windows10:         {search: "Windows 10 22H2", edition: "Core"},
	WindowsServer2025: {search: "Windows Server 2025", edition: "ServerStandard"},
	WindowsServer2022: {search: "Windows Server 2022", edition: "ServerStandard"},
}

// UUP Dump API response types.

// uupdumpBuild is one entry in the listid response. The API returns builds for
// every architecture (amd64, arm64) mixed together, newest first-ish, so Arch
// and Created are needed to pick the right one deterministically.
type uupdumpBuild struct {
	UUID    string `json:"uuid"`
	Title   string `json:"title"`
	Arch    string `json:"arch"`
	Created int64  `json:"created"`
}

// uupdumpListResponse models the listid.php response. `builds` used to be a
// JSON array but is now an object keyed by an opaque index
// (`{"768": {...}, "769": {...}}`), so it is decoded as a map and the values
// are collected.
type uupdumpListResponse struct {
	Response struct {
		Builds map[string]uupdumpBuild `json:"builds"`
	} `json:"response"`
}

type uupdumpFileEntry struct {
	URL string `json:"url"`
}

type uupdumpGetResponse struct {
	Response struct {
		Files map[string]uupdumpFileEntry `json:"files"`
	} `json:"response"`
}

// WindowsImage downloads a Windows ISO via UUP dump and assembles it inside a container.
type WindowsImage struct {
	*BaseImage
	version WindowsVersion
	params  windowsParams
}

// NewWindowsImage constructs a WindowsImage for the given version.
func NewWindowsImage(p provider.Provider, version WindowsVersion) *WindowsImage {
	return &WindowsImage{
		BaseImage: NewBaseImage(p),
		version:   version,
		params:    windowsVersionParams[version],
	}
}

func (w *WindowsImage) Distro() string  { return DistroWindows }
func (w *WindowsImage) Version() string { return string(w.version) }
func (w *WindowsImage) Name() string {
	return fmt.Sprintf("windows-%s.iso", w.version)
}

func (w *WindowsImage) AbsolutePath() string {
	return filepath.Join(w.basePath, w.Name())
}

func (w *WindowsImage) Delete() error {
	if _, err := os.Stat(w.AbsolutePath()); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(w.AbsolutePath())
}

// Download fetches the Windows ISO via UUP dump and container-based ISO assembly.
// Skips if the ISO already exists at the cache path.
func (w *WindowsImage) Download(ctx context.Context) error {
	log := w.provider.Logger()
	log.Info("downloading windows image", zap.String("version", string(w.version)))

	if _, err := os.Stat(w.AbsolutePath()); err == nil {
		log.Info("skipping download", zap.String("file", w.AbsolutePath()), zap.String("reason", "already downloaded"))
		return nil
	}

	uuid, err := w.fetchBuildUUID(ctx)
	if err != nil {
		return fmt.Errorf("uupdump list builds: %w", err)
	}
	log.Info("resolved UUP dump build", zap.String("uuid", uuid))

	esdURLs, err := w.fetchESDURLs(ctx, uuid)
	if err != nil {
		return fmt.Errorf("uupdump get links: %w", err)
	}
	log.Info("resolved ESD files", zap.Int("count", len(esdURLs)))

	if err := w.buildISOInContainer(ctx, esdURLs); err != nil {
		return fmt.Errorf("build windows ISO: %w", err)
	}

	if _, err := os.Stat(w.AbsolutePath()); err != nil {
		return fmt.Errorf("container finished but ISO not found at %s", w.AbsolutePath())
	}

	log.Info("windows ISO ready", zap.String("path", w.AbsolutePath()))
	return nil
}

func (w *WindowsImage) fetchBuildUUID(ctx context.Context) (string, error) {
	apiURL := fmt.Sprintf(uupdumpListURL,
		url.QueryEscape(w.params.search),
		uupdumpLang,
		w.params.edition,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}

	hc := utils.DirectHTTPClientWithTimeout(30 * time.Second)
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("UUP dump list request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("UUP dump list HTTP %d", resp.StatusCode)
	}

	var result uupdumpListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode UUP dump list response: %w", err)
	}

	if len(result.Response.Builds) == 0 {
		return "", fmt.Errorf("no builds found for %q / %s", w.params.search, w.params.edition)
	}

	// UUP lists two build kinds per version: a "Feature update to ..." entry,
	// whose ESD set is a full, self-contained image, and a "Cumulative Update
	// for ..." entry, which is a *differential* pack. Building an ISO from the
	// differential fails in wimlib with "blob not found" (error 55) because its
	// file blobs live in a baseline that isn't downloaded. Always prefer the
	// full feature-update build and never select a cumulative-update one.
	//
	// The API mixes architectures; among eligible builds pick the newest by
	// Created (not map order, which is unspecified) to stay deterministic.
	isCumulative := func(title string) bool {
		return strings.Contains(strings.ToLower(title), "cumulative update")
	}
	isFeatureUpdate := func(title string) bool {
		return strings.Contains(strings.ToLower(title), "feature update")
	}

	var best uupdumpBuild
	found := false
	consider := func(b uupdumpBuild) {
		if !found || b.Created > best.Created {
			best = b
			found = true
		}
	}
	// First pass: full feature-update builds only.
	for _, b := range result.Response.Builds {
		if b.Arch != "" && b.Arch != "amd64" {
			continue
		}
		if isFeatureUpdate(b.Title) {
			consider(b)
		}
	}
	// Fallback: any non-cumulative amd64 build (covers Server ISOs, whose titles
	// don't say "Feature update"). Cumulative-only differentials stay excluded.
	if !found {
		for _, b := range result.Response.Builds {
			if b.Arch != "" && b.Arch != "amd64" {
				continue
			}
			if !isCumulative(b.Title) {
				consider(b)
			}
		}
	}
	if !found {
		return "", fmt.Errorf("no full (non-cumulative) amd64 build found for %q / %s", w.params.search, w.params.edition)
	}

	w.provider.Logger().Info("found UUP dump build", zap.String("title", best.Title), zap.String("uuid", best.UUID))
	return best.UUID, nil
}

func (w *WindowsImage) fetchESDURLs(ctx context.Context, uuid string) (map[string]string, error) {
	apiURL := fmt.Sprintf(uupdumpGetURL, uuid, uupdumpLang, w.params.edition)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}

	hc := utils.DirectHTTPClientWithTimeout(30 * time.Second)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("UUP dump get request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("UUP dump get HTTP %d", resp.StatusCode)
	}

	var result uupdumpGetResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode UUP dump get response: %w", err)
	}

	out := make(map[string]string, len(result.Response.Files))
	for name, entry := range result.Response.Files {
		if entry.URL != "" {
			out[name] = entry.URL
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no ESD download URLs returned for uuid %s", uuid)
	}

	return out, nil
}

func (w *WindowsImage) buildISOInContainer(ctx context.Context, esdURLs map[string]string) error {
	runtime, err := findWindowsContainerRuntime()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(w.basePath, 0o755); err != nil {
		return fmt.Errorf("create iso cache dir: %w", err)
	}

	// Build the work dir alongside the ISO cache rather than in $TMPDIR. The
	// multi-GB ESD download plus the exported install.wim easily exceed a
	// RAM-backed /tmp (tmpfs), which would fail mid-download with ENOSPC; the
	// ISO cache lives on real disk.
	workDir, err := os.MkdirTemp(w.basePath, ".vee-windows-"+string(w.version)+"-")
	if err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	// Build a wget command for each ESD file.
	var wgetLines []string
	for filename, cdnURL := range esdURLs {
		safe := strings.ReplaceAll(filename, "'", "'\\''")
		safeURL := strings.ReplaceAll(cdnURL, "'", "'\\''")
		wgetLines = append(wgetLines, fmt.Sprintf("wget -q --show-progress -O /work/esd/'%s' '%s'", safe, safeURL))
	}

	buildScript := `set -e
apk add --no-cache wimlib xorriso wget ca-certificates >/dev/null 2>&1
mkdir -p /work/esd /work/iso/sources /work/iso/boot /work/iso/efi

# Download all ESD files
` + strings.Join(wgetLines, "\n") + `

# Find the install ESD (largest .esd file)
INSTALL_ESD=$(ls -S /work/esd/*.esd 2>/dev/null | head -1)
if [ -z "$INSTALL_ESD" ]; then
    echo "ERROR: no .esd files found in /work/esd" >&2
    ls /work/esd/ >&2
    exit 1
fi
echo "Using install ESD: $INSTALL_ESD"

# UUP ships Windows as a set of ESDs where the install ESD is a delta WIM whose
# file blobs live in sibling ESDs. Every wimlib operation on it must reference
# the whole set via --ref so those blobs resolve; without it wimlib fails with
# "blob not found" (error 55).
REFGLOB="/work/esd/*.esd"

# Extract boot files from ESD index 1 (Windows PE / boot)
wimlib-imagex extract "$INSTALL_ESD" 1 /Windows/Boot/EFI/ /work/iso/efi/ --ref="$REFGLOB" --no-acls 2>/dev/null || true
wimlib-imagex extract "$INSTALL_ESD" 1 /Windows/Boot/PCAT/ /work/iso/boot/ --ref="$REFGLOB" --no-acls 2>/dev/null || true

# Export install.wim from all available indexes, resolving blobs across the set.
INDEXES=$(wimlib-imagex info "$INSTALL_ESD" | grep '^Index' | awk '{print $2}')
for IDX in $INDEXES; do
    wimlib-imagex export "$INSTALL_ESD" "$IDX" /work/iso/sources/install.wim \
        --ref="$REFGLOB" --compress=LZX
done

# Assemble bootable ISO with xorriso
xorriso -as mkisofs \
    -iso-level 3 \
    -full-iso9660-filenames \
    -volid "WIN_INSTALL" \
    -eltorito-boot boot/etfsboot.com \
    -eltorito-catalog boot/boot.cat \
    -no-emul-boot -boot-load-seg 0x07C0 -boot-load-size 8 \
    -eltorito-alt-boot \
    -e efi/microsoft/boot/efisys_noprompt.bin \
    -no-emul-boot \
    -o /out/` + w.Name() + ` \
    /work/iso
`

	args := []string{
		"run", "--rm",
		"--network=host",
		"-v", workDir + ":/work",
		"-v", w.basePath + ":/out",
		"alpine:latest",
		"sh", "-c", buildScript,
	}

	cmd := exec.CommandContext(ctx, runtime, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func findWindowsContainerRuntime() (string, error) {
	for _, name := range []string{"nerdctl", "docker"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("windows ISO build requires nerdctl or docker (none found in PATH)")
}
