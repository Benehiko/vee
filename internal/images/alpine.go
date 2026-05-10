package images

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Benehiko/vee/internal/utils"
	"github.com/Benehiko/vee/provider"
	"github.com/codingsince1985/checksum"
	"go.uber.org/zap"
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
	target := ""
	for line := range strings.SplitSeq(string(body), "\n") {
		if strings.Contains(line, "nocloud_alpine-") &&
			strings.Contains(line, "x86_64-bios-cloudinit") &&
			strings.Contains(line, ".qcow2\"") &&
			!strings.Contains(line, ".sha256") {
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

	// Fetch SHA256 checksum for this file.
	checksumURL := indexURL + target + ".sha256"
	req, err = http.NewRequestWithContext(ctx, "GET", checksumURL, nil)
	if err != nil {
		return err
	}
	resp, err = httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	checksumBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	targetChecksum := strings.Fields(string(checksumBody))[0]

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

	if err := os.MkdirAll(a.basePath, 0o755); err != nil {
		return err
	}

	destPath := filepath.Join(a.basePath, target)
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := a.CreateImage(f, resp.Body, resp.ContentLength); err != nil {
		return err
	}

	sha256, err := checksum.SHA256sum(destPath)
	if err != nil {
		return err
	}
	if sha256 != targetChecksum {
		return fmt.Errorf("alpine checksum mismatch: expected %s, got %s", targetChecksum, sha256)
	}

	// Rename to our stable name so AbsolutePath() always points to it.
	if err := os.Rename(destPath, a.AbsolutePath()); err != nil {
		return err
	}

	a.provider.Logger().Info("alpine image ready", zap.String("path", a.AbsolutePath()))
	return nil
}
