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

	if err := os.MkdirAll(w.basePath, 0o750); err != nil {
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

	// A UUP set is not a single install image: the "<edition>_<lang>.esd" file
	// (e.g. core_en-us.esd) is the metadata ESD that indexes the OS image, whose
	// actual file blobs are spread across the sibling "Microsoft-Windows-*.ESD"
	// component packages. The install image lives at index 3 of the metadata
	// ESD; WinPE is index 1 and WinRE is index 2. Exporting must name the
	// metadata ESD as the source and --ref the whole set so blobs resolve. The
	// old "largest ESD" heuristic picked a component package instead, which
	// failed with "blob not found" (wimlib error 55).
	metaESD := fmt.Sprintf("%s_%s.esd", strings.ToLower(w.params.edition), uupdumpLang)

	buildScript := `set -e
apk add --no-cache wimlib xorriso cabextract wget ca-certificates >/dev/null 2>&1
mkdir -p /work/esd /work/ref /work/iso/sources /work/iso/boot /work/iso/efi

# Download all UUP files (ESDs and component-package CABs).
` + strings.Join(wgetLines, "\n") + `

# A UUP set does not contain a ready install.wim. The install image's file blobs
# are spread across two kinds of component packages: sibling ESDs and CAB files.
# wimlib resolves cross-container blobs via --ref, but CABs must first be
# extracted and captured into interim ESDs (mirrors uup-converter-wimlib). Build
# a single reference directory holding every ESD plus the captured CAB ESDs.
#
# UUP ships component ESDs with an uppercase ".ESD" extension, so all globbing
# here is case-insensitive; a case-sensitive "*.esd" glob silently drops them
# and their blobs, causing "blob not found" (wimlib error 55).
for f in /work/esd/*.esd /work/esd/*.ESD; do
    [ -f "$f" ] && ln -sf "$f" /work/ref/"$(basename "$f")"
done

CABN=0
for cab in /work/esd/*.cab /work/esd/*.CAB; do
    [ -f "$cab" ] || continue
    base=$(basename "$cab")
    dir="/tmp/cabx/$base.d"
    mkdir -p "$dir"
    if cabextract -q -d "$dir" "$cab" >/dev/null 2>&1; then
        if wimlib-imagex capture "$dir" /work/ref/"$base.esd" \
            --no-acls --norpfix "pkg" "pkg" >/dev/null 2>&1; then
            CABN=$((CABN+1))
        fi
    fi
    rm -rf "$dir"
done
echo "prepared reference set: $(ls /work/ref | wc -l) images ($CABN from CABs)"

# Locate the metadata ESD "<edition>_<lang>.esd" (case-insensitive). It indexes
# the OS image (index 3); WinPE is index 1 and WinRE is index 2.
META="` + metaESD + `"
INSTALL_ESD=$(ls /work/ref/ | grep -i -x "$META" | head -1)
if [ -n "$INSTALL_ESD" ]; then
    INSTALL_ESD="/work/ref/$INSTALL_ESD"
else
    # Fallback: the sole ESD not shipped as a Microsoft-Windows-* component pack.
    INSTALL_ESD=$(ls /work/ref/*.esd /work/ref/*.ESD 2>/dev/null | grep -vi '/Microsoft' | grep -vi '/ModernApps' | head -1)
fi
if [ -z "$INSTALL_ESD" ] || [ ! -f "$INSTALL_ESD" ]; then
    echo "ERROR: could not locate metadata ESD ($META) in /work/ref" >&2
    ls -S /work/ref/ >&2
    exit 1
fi
echo "Using metadata ESD: $INSTALL_ESD"

# The reference directory holds only WIM/ESD images, so a plain "*" is safe.
REFGLOB="/work/ref/*"

# Index 1 ("Windows Setup Media") is the full bootable ISO tree: bootmgr, /boot,
# /efi, setup.exe and an empty /sources. Apply it as the ISO root, then drop the
# OS install image into /sources.
wimlib-imagex apply "$INSTALL_ESD" 1 /work/iso --ref="$REFGLOB" --no-acls 2>/dev/null
mkdir -p /work/iso/sources

# Create sources/boot.wim — the WinPE image the DVD boots into to run Setup.
# Without it the firmware loads bootmgr but WinPE cannot start and Windows Boot
# Manager fails with 0xc000000f ("a required device isn't connected"). Applying
# the setup media above lays down an EMPTY /sources, so boot.wim must be exported
# here.
#
# Preferred source is a dedicated *Setup* WinPE image (one that already launches
# setup.exe). Current win11 24H2 UUP sets, however, ship no such image in the
# metadata ESD — only "Windows Setup Media" (the ISO tree), the "Recovery
# Environment" (WinRE), and the OS editions. WinRE by itself boots into the
# "Choose an option / Troubleshoot" recovery menu (its winpeshl.ini launches
# X:\sources\recovery\recenv.exe), so it must be TRANSFORMED into a Setup boot
# environment before use. Match the boot image by NAME (index numbering is not
# stable across sets), preferring a real Setup/PE image and falling back to WinRE.
BOOT_IDX=$(wimlib-imagex info "$INSTALL_ESD" 2>/dev/null | awk '
    tolower($0) ~ /^index:/ {idx=$2}
    tolower($0) ~ /^name:/ && tolower($0) !~ /setup media/ &&
        (tolower($0) ~ /windows pe/ || tolower($0) ~ /windows setup/) {
        print idx; exit
    }')
IS_WINRE=0
if [ -z "$BOOT_IDX" ]; then
    # Fall back to the Recovery Environment image and convert it to Setup below.
    BOOT_IDX=$(wimlib-imagex info "$INSTALL_ESD" 2>/dev/null | awk '
        tolower($0) ~ /^index:/ {idx=$2}
        tolower($0) ~ /^name:/ && tolower($0) ~ /recovery environment/ {print idx; exit}')
    IS_WINRE=1
fi
if [ -z "$BOOT_IDX" ]; then
    echo "ERROR: no WinPE/Setup/Recovery image found in the metadata ESD; cannot build boot.wim" >&2
    wimlib-imagex info "$INSTALL_ESD" | grep -iE '^(index|name):' >&2
    exit 1
fi
# Export the chosen image as boot.wim, marked bootable — the image Windows Boot
# Manager launches.
wimlib-imagex export "$INSTALL_ESD" "$BOOT_IDX" /work/iso/sources/boot.wim \
    --ref="$REFGLOB" --compress=LZX --boot 2>/dev/null

if [ "$IS_WINRE" = "1" ]; then
    # Transform WinRE into a Windows Setup boot environment. WinRE's winpeshl.ini
    # hard-launches the recovery shell (recenv.exe); remove it and install a
    # startnet.cmd that runs wpeinit then launches setup.exe. This is the same
    # result a native Setup boot.wim produces — the guest boots straight into
    # "Windows Setup — Select language settings".
    #
    # Windows 11 24H2 "ConX" Setup (sources\setuphost.exe) writes its
    # $WINDOWS.~BT working/diagnostics tree onto the drive setup.exe runs from.
    # The install DVD is read-only, so launching setup.exe from it fails with
    # "Access is denied [0x00000005]" and the cascading fatal "OneSettings
    # initialization failed 0x800702E7" (~318 MB into image apply — issue #17).
    # Fix: copy the DVD tree onto a writable scratch virtio disk and launch
    # setup.exe from there so $WINDOWS.~BT lands on writable media.
    #
    # startnet.cmd: drvload viostor (WinRE has no virtio-blk driver) so the
    # scratch virtio disk is visible -> select the last disk (the scratch disk
    # is added last, so it enumerates as the highest index; the OS disk is disk
    # 0) -> format NTFS as W: -> xcopy the DVD onto W: -> run W:\setup.exe.
    # Everything is version-agnostic (scan for viostor.inf / the DVD letter) so
    # this heredoc needs no per-version data. Falls back to the old direct-DVD
    # launch if viostor or the DVD cannot be found (no worse than before for the
    # win10 classic-Setup path, which does not hit the 24H2 gate anyway).
    #
    # NOTE: this runs in a WinRE-derived WinPE, which ships a REDUCED toolset —
    # findstr.exe and robocopy.exe are NOT present. Only find.exe and xcopy.exe
    # are available for filtering/copying, so this script must avoid findstr and
    # robocopy entirely.
    cat > /tmp/startnet.cmd <<'STARTNET'
@echo off
wpeinit
set LOG=X:\startnet.log
echo [startnet] begin > %LOG%

rem 1. Load the virtio-blk (viostor) driver so WinPE can see the virtio disks.
rem    The extras CD ships viostor.inf for MANY Windows versions
rem    (\viostor\<ver>\amd64\viostor.inf: 2k12, 2k16, w10, w11, ...). The
rem    correct one for this WinPE is not knowable here, and picking the wrong
rem    version's inf makes drvload fail so the virtio disks stay invisible.
rem    Both the OS disk and the scratch disk are virtio, so a failed drvload
rem    means diskpart sees NO usable disk. Load EVERY viostor.inf found — only
rem    the matching one binds; the rest are harmless no-ops.
set FOUNDVIO=
for %%d in (C D E F G H I J K L M N O P Q R S T U V W X Y Z) do (
  if exist %%d:\viostor (
    for /f "delims=" %%f in ('dir /s /b %%d:\viostor\viostor.inf 2^>nul') do (
      set FOUNDVIO=1
      echo [startnet] drvload %%f >> %LOG%
      drvload "%%f" >> %LOG% 2>&1
    )
  )
)
if not defined FOUNDVIO echo [startnet] WARNING no viostor.inf found >> %LOG%

rem 2. Identify the scratch disk. It is the last virtio disk added to the
rem    machine (the OS disk is disk 0, the 8 GB scratch is the highest index).
rem    findstr is unavailable in WinRE, so filter diskpart output with find.exe.
rem    "find" also matches the "Disk ###" header row, so drop it with a second
rem    "find /v" on "#", then keep the last remaining index = highest = scratch.
echo list disk > X:\dp-list.txt
diskpart /s X:\dp-list.txt > X:\dp-out.txt
type X:\dp-out.txt >> %LOG%
set SCRATCH=
for /f "tokens=2" %%a in ('type X:\dp-out.txt ^| find "Disk " ^| find /v "#"') do set SCRATCH=%%a
echo [startnet] scratch disk = %SCRATCH% >> %LOG%
if not defined SCRATCH (
  echo [startnet] ERROR no virtio disk visible ^(drvload failed?^), aborting to DVD >> %LOG%
  goto DVDFALLBACK
)

rem 3. Clean + GPT + NTFS-format the scratch disk, assign letter W:.
echo select disk %SCRATCH% > X:\dp-fmt.txt
echo clean >> X:\dp-fmt.txt
echo convert gpt >> X:\dp-fmt.txt
echo create partition primary >> X:\dp-fmt.txt
echo format fs=ntfs quick label=VEESCRATCH >> X:\dp-fmt.txt
echo assign letter=W >> X:\dp-fmt.txt
diskpart /s X:\dp-fmt.txt >> %LOG% 2>&1

rem 4. Find the install DVD (setup.exe + sources\install.wim) and copy it to W:.
rem    robocopy is unavailable in WinRE; use xcopy (/e all subdirs, /h hidden,
rem    /y overwrite, /c continue on error, /q quiet). Deliberately NOT /k: the
rem    DVD files carry the read-only attribute, and copying that onto W: makes
rem    24H2 ConX Setup fail 0x80070103-0x40031 when it tries to rewrite files in
rem    its own working tree. After copying, clear read-only recursively so the
rem    whole tree is writable.
set DVD=
for %%d in (C D E F G H I J K L M N O P Q R S T U V W X Y Z) do (
  if not defined DVD if exist %%d:\setup.exe if exist %%d:\sources\install.wim set DVD=%%d
)
echo [startnet] dvd = %DVD% >> %LOG%
if not defined DVD goto DVDSCAN
if not exist W:\ (
  echo [startnet] ERROR W: not formatted, falling back to DVD >> %LOG%
  start "" /wait %DVD%:\setup.exe
  goto END
)
echo [startnet] xcopy %DVD%:\ W:\ >> %LOG%
xcopy %DVD%:\*.* W:\ /e /h /y /c /q >> %LOG% 2>&1
echo [startnet] clearing read-only attributes on W: >> %LOG%
attrib -R W:\*.* /S /D >> %LOG% 2>&1
if exist W:\setup.exe (
  echo [startnet] launch W:\setup.exe >> %LOG%
  start "" /wait W:\setup.exe
) else (
  echo [startnet] W:\setup.exe missing, falling back to DVD >> %LOG%
  start "" /wait %DVD%:\setup.exe
)
rem After Setup returns (success reboots before reaching here; failure falls
rem through), harvest Setup's Panther logs from the WinPE RAM disk (X:) and any
rem other drive to W:\vee-logs so they survive for offline inspection.
mkdir W:\vee-logs >nul 2>&1
copy %LOG% W:\vee-logs\startnet.log >nul 2>&1
for %%d in (X C D E F G H I J K L M N O P Q R S T U V W Y Z) do (
  if exist %%d:\$WINDOWS.~BT\Sources\Panther xcopy %%d:\$WINDOWS.~BT\Sources\Panther\*.* W:\vee-logs\panther-%%d\ /e /h /y /c /q >nul 2>&1
)
goto END

:DVDSCAN
echo [startnet] no DVD matched, scanning for setup.exe >> %LOG%
:DVDFALLBACK
rem The writable-scratch path could not be set up; fall back to launching Setup
rem directly from the (read-only) DVD. On 24H2 this hits the OneSettings gate,
rem but it is no worse than the pre-fix behaviour and keeps older media working.
for %%d in (C D E F G H I J K L M N O P Q R S T U V W X Y Z) do if exist %%d:\setup.exe start "" /wait %%d:\setup.exe
:END
STARTNET
    wimlib-imagex update /work/iso/sources/boot.wim 1 \
        --command="delete --force /Windows/System32/winpeshl.ini" >/dev/null 2>&1 || true
    wimlib-imagex update /work/iso/sources/boot.wim 1 \
        --command="add /tmp/startnet.cmd /Windows/System32/startnet.cmd" >/dev/null 2>&1
fi

# Export the OS install image (index 3) into install.wim, resolving blobs across
# the whole reference set (base ESDs + captured CAB ESDs).
wimlib-imagex export "$INSTALL_ESD" 3 /work/iso/sources/install.wim \
    --ref="$REFGLOB" --compress=LZX

# Inject WinRE (winre.wim) into install.wim at \Windows\System32\Recovery.
#
# UUP metadata ESDs keep the Recovery Environment as a SEPARATE image; a plain
# export of the OS image (index 3) leaves \Windows\System32\Recovery WITHOUT a
# winre.wim (only ReAgent.xml). Real Microsoft media bundles winre.wim inside
# install.wim, and Windows 11 24H2 Setup's image-deploy / SafeOS step hard-
# requires it: without it Setup fails extracting
# "\Windows\System32\Recovery\winre.wim from install.wim" with
# 0x80070003 (ERROR_PATH_NOT_FOUND) right after the image starts deploying
# (issue #17). So export the pristine Recovery Environment image from the ESD to
# a standalone winre.wim and add it into install.wim at the expected path.
#
# Use the Recovery Environment image by NAME (not BOOT_IDX: when a dedicated
# Setup PE exists, BOOT_IDX points at that, not WinRE). This is the untouched
# recovery image — NOT the Setup-transformed boot.wim above.
WINRE_IDX=$(wimlib-imagex info "$INSTALL_ESD" 2>/dev/null | awk '
    tolower($0) ~ /^index:/ {idx=$2}
    tolower($0) ~ /^name:/ && tolower($0) ~ /recovery environment/ {print idx; exit}')
if [ -n "$WINRE_IDX" ]; then
    echo "Injecting WinRE (index $WINRE_IDX) into install.wim as winre.wim"
    wimlib-imagex export "$INSTALL_ESD" "$WINRE_IDX" /tmp/winre.wim \
        --ref="$REFGLOB" --compress=LZX 2>/dev/null
    # install.wim has a single image (index 1). Add winre.wim into it; create the
    # Recovery dir path if the update command needs it (the dir already exists in
    # the OS image, so a plain add of the file is sufficient).
    wimlib-imagex update /work/iso/sources/install.wim 1 \
        --command="add /tmp/winre.wim /Windows/System32/Recovery/winre.wim" >/dev/null 2>&1
    if wimlib-imagex dir /work/iso/sources/install.wim 1 2>/dev/null | grep -qi "/Windows/System32/Recovery/winre.wim"; then
        echo "WinRE injected into install.wim OK"
    else
        echo "ERROR: failed to inject winre.wim into install.wim" >&2
        exit 1
    fi
else
    echo "WARNING: no Recovery Environment image in ESD; install.wim will lack winre.wim (24H2 Setup may fail 0x80070003)" >&2
fi

if [ ! -f /work/iso/boot/etfsboot.com ] || [ ! -f /work/iso/efi/microsoft/boot/efisys_noprompt.bin ]; then
    echo "ERROR: boot files missing after applying setup media" >&2
    exit 1
fi
if [ ! -f /work/iso/sources/boot.wim ]; then
    echo "ERROR: sources/boot.wim was not produced — media would be unbootable" >&2
    exit 1
fi

# Assemble a hybrid BIOS + UEFI bootable ISO. etfsboot.com is the BIOS El Torito
# boot image; efisys_noprompt.bin is the UEFI one (no "press any key" prompt).
xorriso -as mkisofs \
    -iso-level 3 \
    -full-iso9660-filenames \
    -volid "WIN_INSTALL" \
    -b boot/etfsboot.com \
    -no-emul-boot -boot-load-size 8 -boot-info-table \
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

	//nolint:gosec // runtime is a container CLI resolved via LookPath; args are internally built to run the ISO build in a container. This is core VM-manager functionality.
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
