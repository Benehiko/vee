package images

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/utils"
	"github.com/Benehiko/vee/provider"
)

// AlpineVersion is a minor version string like "3.21".
type AlpineVersion string

// KnownAlpineVersions lists supported Alpine versions, newest first.
var KnownAlpineVersions = []AlpineVersion{
	"3.21",
	"3.20",
	"3.19",
}

type AlpineImage struct {
	*BaseImage
	version AlpineVersion
}

func NewAlpineImage(p provider.Provider, version AlpineVersion) *AlpineImage {
	return &AlpineImage{
		BaseImage: NewBaseImage(p),
		version:   version,
	}
}

func (a *AlpineImage) Distro() string  { return DistroAlpine }
func (a *AlpineImage) Version() string { return string(a.version) }

func (a *AlpineImage) Name() string {
	return fmt.Sprintf("nocloud_alpine-%s-x86_64-bios-cloudinit-r0.qcow2", a.fullVersion())
}

func (a *AlpineImage) AbsolutePath() string {
	return filepath.Join(a.basePath, a.Name())
}

func (a *AlpineImage) Delete() error {
	if _, err := os.Stat(a.AbsolutePath()); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(a.AbsolutePath())
}

// fullVersion resolves the patch version from the release index.
// Alpine cloud image filenames use the full semver (e.g. "3.21.3").
// We fetch the checksum file name to discover the exact patch release.
func (a *AlpineImage) fullVersion() string {
	// The minor version is also used as the full version placeholder;
	// the actual patch-level filename is discovered at download time.
	// For the name/path we use the minor version and let Download resolve it.
	return string(a.version)
}

func (a *AlpineImage) Download(ctx context.Context) error {
	if _, err := os.Stat(a.AbsolutePath()); err == nil {
		a.provider.Logger().Info("skipping download",
			zap.String("file", a.AbsolutePath()),
			zap.String("reason", "already downloaded"))
		return nil
	}

	// Fetch the directory listing to find the latest patch release for this minor.
	indexURL := fmt.Sprintf("https://dl-cdn.alpinelinux.org/alpine/v%s/releases/cloud/", a.version)
	httpClient := utils.DirectHTTPClient()

	req, err := http.NewRequestWithContext(ctx, "GET", indexURL, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// Find the newest nocloud qcow2 cloudinit image for bios/x86_64.
	// Exclude the "-metal-" variant, which shares the "bios-cloudinit" substring
	// but is a different image than the one Name() expects.
	target := ""
	for line := range strings.SplitSeq(string(body), "\n") {
		if strings.Contains(line, "nocloud_alpine-") &&
			strings.Contains(line, "x86_64-bios-cloudinit-r") &&
			!strings.Contains(line, "-metal-") &&
			strings.Contains(line, ".qcow2\"") &&
			!strings.Contains(line, ".sha512") &&
			!strings.Contains(line, ".asc") {
			// Extract filename from href="..."
			start := strings.Index(line, `href="`)
			if start < 0 {
				continue
			}
			start += len(`href="`)
			end := strings.Index(line[start:], `"`)
			if end < 0 {
				continue
			}
			candidate := line[start : start+end]
			if candidate > target {
				target = candidate
			}
		}
	}
	if target == "" {
		return fmt.Errorf("alpine: no cloud image found in %s", indexURL)
	}

	// Fetch the SHA512 checksum for this file. Alpine cloud images publish a
	// bare-hash ".sha512" sidecar (no ".sha256" is offered).
	checksumURL := indexURL + target + ".sha512"
	req, err = http.NewRequestWithContext(ctx, "GET", checksumURL, nil)
	if err != nil {
		return err
	}
	resp, err = httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("alpine: fetch checksum %s: HTTP %d", checksumURL, resp.StatusCode)
	}

	checksumBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(checksumBody))
	if len(fields) == 0 {
		return fmt.Errorf("alpine: empty checksum file at %s", checksumURL)
	}
	targetChecksum := strings.ToLower(fields[0])
	// A valid SHA512 is 128 hex chars; guard against an error page slipping through.
	if len(targetChecksum) != 128 {
		return fmt.Errorf("alpine: unexpected checksum content at %s (got %q)", checksumURL, targetChecksum)
	}

	// Download the image.
	imgURL := indexURL + target
	req, err = http.NewRequestWithContext(ctx, "GET", imgURL, nil)
	if err != nil {
		return err
	}
	resp, err = httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if err := os.MkdirAll(a.basePath, 0o750); err != nil {
		return err
	}

	destPath := filepath.Join(a.basePath, target)
	//nolint:gosec // destPath is basePath (internal) joined with a target derived from the signed Alpine index, not user input.
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := a.CreateImage(f, resp.Body, resp.ContentLength); err != nil {
		return err
	}

	sum, err := sha512File(destPath)
	if err != nil {
		return err
	}
	if sum != targetChecksum {
		return fmt.Errorf("alpine checksum mismatch: expected %s, got %s", targetChecksum, sum)
	}

	// Rename to our stable name so AbsolutePath() always points to it.
	if err := os.Rename(destPath, a.AbsolutePath()); err != nil {
		return err
	}

	a.provider.Logger().Info("alpine image ready", zap.String("path", a.AbsolutePath()))
	return nil
}

// sha512File returns the lowercase hex SHA512 digest of the file at path.
func sha512File(path string) (string, error) {
	//nolint:gosec // path is an internally constructed image cache path, not user-controlled input.
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
