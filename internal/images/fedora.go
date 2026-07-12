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

const (
	// FedoraReleaseDirURL is the per-release ISO directory. The exact ISO and
	// CHECKSUM filenames (which embed a per-release build number such as "1.1"
	// or "1.4") are discovered by scraping this listing rather than hardcoded.
	FedoraReleaseDirURL = "https://download.fedoraproject.org/pub/fedora/linux/releases/%s/Server/x86_64/iso/"
)

// FedoraVersion is a Fedora release number like "42".
type FedoraVersion string

// KnownFedoraVersions lists supported Fedora versions, newest first.
// Only releases still served under /releases/ are listed; older releases are
// moved to archives.fedoraproject.org under a different URL.
var KnownFedoraVersions = []FedoraVersion{
	"42",
	"41",
}

type FedoraImage struct {
	*BaseImage
	version FedoraVersion

	// isoName is the ISO filename resolved from the release listing at download
	// time (e.g. "Fedora-Server-dvd-x86_64-42-1.1.iso"). Empty until resolved.
	isoName string
}

func NewFedoraImage(p provider.Provider, version FedoraVersion) *FedoraImage {
	return &FedoraImage{
		BaseImage: NewBaseImage(p),
		version:   version,
	}
}

func (f *FedoraImage) Distro() string  { return "fedora" }
func (f *FedoraImage) Version() string { return string(f.version) }

// Name returns the resolved ISO filename once known, otherwise a stable
// placeholder scoped to the version so the cache path is deterministic.
func (fi *FedoraImage) Name() string {
	if fi.isoName != "" {
		return fi.isoName
	}
	return fmt.Sprintf("Fedora-Server-dvd-x86_64-%s.iso", fi.version)
}

func (fi *FedoraImage) AbsolutePath() string {
	return filepath.Join(fi.basePath, fi.Name())
}

func (fi *FedoraImage) Delete() error {
	if _, err := os.Stat(fi.AbsolutePath()); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(fi.AbsolutePath())
}

func (fi *FedoraImage) checksum() (string, error) {
	return checksum.SHA256sum(fi.AbsolutePath())
}

// resolveNames scrapes the release ISO directory and returns the DVD ISO and
// CHECKSUM filenames for this version.
func (fi *FedoraImage) resolveNames(ctx context.Context, client *http.Client) (isoName, checksumName string, err error) {
	dirURL := fmt.Sprintf(FedoraReleaseDirURL, fi.version)
	req, err := http.NewRequestWithContext(ctx, "GET", dirURL, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("fedora: list %s: HTTP %d", dirURL, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	for _, href := range hrefsIn(string(body)) {
		switch {
		case strings.HasPrefix(href, "Fedora-Server-dvd-x86_64-") && strings.HasSuffix(href, ".iso"):
			isoName = href
		case strings.HasPrefix(href, "Fedora-Server-") && strings.HasSuffix(href, "-x86_64-CHECKSUM"):
			checksumName = href
		}
	}
	if isoName == "" || checksumName == "" {
		return "", "", fmt.Errorf("fedora: could not find ISO/CHECKSUM in %s", dirURL)
	}
	return isoName, checksumName, nil
}

func (fi *FedoraImage) Download(ctx context.Context) error {
	fi.provider.Logger().Info("downloading", zap.String("distro", "fedora"), zap.String("version", string(fi.version)))

	httpClient := utils.DirectHTTPClient()
	dirURL := fmt.Sprintf(FedoraReleaseDirURL, fi.version)

	isoName, checksumName, err := fi.resolveNames(ctx, httpClient)
	if err != nil {
		return err
	}
	fi.isoName = isoName

	// Fetch the CHECKSUM file.
	req, err := http.NewRequestWithContext(ctx, "GET", dirURL+checksumName, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fedora: fetch checksum %s: HTTP %d", dirURL+checksumName, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// CHECKSUM file format: SHA256 (filename) = hash
	var targetChecksum string
	for line := range strings.SplitSeq(string(b), "\n") {
		if strings.Contains(line, isoName) && strings.HasPrefix(strings.TrimSpace(line), "SHA256") {
			parts := strings.Split(line, "=")
			if len(parts) == 2 {
				targetChecksum = strings.TrimSpace(parts[1])
			}
			break
		}
	}
	if targetChecksum == "" {
		return fmt.Errorf("checksum not found for %s", isoName)
	}

	if _, err := os.Stat(fi.AbsolutePath()); err == nil {
		sha256, err := fi.checksum()
		if err != nil {
			return err
		}
		if sha256 == targetChecksum {
			fi.provider.Logger().Info("skipping download",
				zap.String("file", fi.AbsolutePath()),
				zap.String("reason", "already downloaded"))
			return nil
		}
		fi.provider.Logger().Warn("removing file due to checksum mismatch",
			zap.String("file", fi.AbsolutePath()),
			zap.String("expected", targetChecksum),
			zap.String("actual", sha256))
		if err := os.Remove(fi.AbsolutePath()); err != nil {
			return err
		}
	}

	req, err = http.NewRequestWithContext(ctx, "GET", dirURL+isoName, nil)
	if err != nil {
		return err
	}
	resp, err = httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fedora: fetch ISO %s: HTTP %d", dirURL+isoName, resp.StatusCode)
	}

	if err := os.MkdirAll(fi.basePath, 0o755); err != nil {
		return err
	}

	f, err := os.Create(fi.AbsolutePath())
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := fi.CreateImage(f, resp.Body, resp.ContentLength); err != nil {
		return err
	}

	sha256, err := fi.checksum()
	if err != nil {
		return err
	}
	if sha256 != targetChecksum {
		fi.provider.Logger().Error("checksum mismatch",
			zap.String("file", fi.AbsolutePath()),
			zap.String("expected", targetChecksum),
			zap.String("actual", sha256))
		return fmt.Errorf("checksum mismatch: expected %s, got %s", targetChecksum, sha256)
	}

	return nil
}
