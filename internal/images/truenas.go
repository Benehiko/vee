package images

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Benehiko/vee/provider"
	"github.com/codingsince1985/checksum"
	"go.uber.org/zap"
)

const (
	// TrueNAS SCALE ISO download URL.
	// Pattern: https://download.sys.truenas.net/TrueNAS-SCALE-<version>/TrueNAS-SCALE-<version>.iso
	TrueNASDownloadURL         = "https://download.sys.truenas.net/TrueNAS-SCALE-%s/TrueNAS-SCALE-%s.iso"
	TrueNASDownloadChecksumURL = "https://download.sys.truenas.net/TrueNAS-SCALE-%s/TrueNAS-SCALE-%s.iso.sha256"
)

type TrueNASVersion string

const (
	TrueNAS2504 TrueNASVersion = "25.04.2.1"
	TrueNAS2410 TrueNASVersion = "24.10.2.4"
)

// KnownTrueNASVersions lists supported TrueNAS SCALE versions, newest first.
var KnownTrueNASVersions = []TrueNASVersion{
	TrueNAS2504,
	TrueNAS2410,
}

type TrueNASImage struct {
	*BaseImage
	version TrueNASVersion
}

func NewTrueNASImage(p provider.Provider, version TrueNASVersion) *TrueNASImage {
	return &TrueNASImage{
		BaseImage: NewBaseImage(p),
		version:   version,
	}
}

func (t *TrueNASImage) Distro() string  { return DistroTrueNAS }
func (t *TrueNASImage) Version() string { return string(t.version) }

func (t *TrueNASImage) Name() string {
	return fmt.Sprintf("TrueNAS-SCALE-%s.iso", t.version)
}

func (t *TrueNASImage) AbsolutePath() string {
	return filepath.Join(t.basePath, t.Name())
}

func (t *TrueNASImage) Delete() error {
	if _, err := os.Stat(t.AbsolutePath()); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(t.AbsolutePath())
}

func (t *TrueNASImage) checksum() (string, error) {
	return checksum.SHA256sum(t.AbsolutePath())
}

func (t *TrueNASImage) Download(ctx context.Context) error {
	t.provider.Logger().Info("downloading", zap.String("file", t.Name()))

	// If the file already exists, try to verify against the remote checksum.
	// If the checksum endpoint is unavailable (404, network error), trust the
	// existing file and skip the download.
	if _, err := os.Stat(t.AbsolutePath()); err == nil {
		httpClient := &http.Client{}
		targetChecksum, err := t.fetchRemoteChecksum(ctx, httpClient)
		if err != nil {
			t.provider.Logger().Warn("skipping checksum verification — remote unavailable",
				zap.String("file", t.AbsolutePath()),
				zap.Error(err))
			return nil
		}
		sha256, err := t.checksum()
		if err != nil {
			return err
		}
		if sha256 == targetChecksum {
			t.provider.Logger().Info("skipping download",
				zap.String("file", t.AbsolutePath()),
				zap.String("reason", "already downloaded"))
			return nil
		}
		t.provider.Logger().Warn("removing file due to checksum mismatch",
			zap.String("file", t.AbsolutePath()),
			zap.String("expected", targetChecksum),
			zap.String("actual", sha256))
		if err := os.Remove(t.AbsolutePath()); err != nil {
			return err
		}
	}

	httpClient := &http.Client{}

	// Fetch remote checksum before downloading.
	targetChecksum, err := t.fetchRemoteChecksum(ctx, httpClient)
	if err != nil {
		return err
	}

	isoURL := fmt.Sprintf(TrueNASDownloadURL, t.version, t.version)
	req, err := http.NewRequestWithContext(ctx, "GET", isoURL, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading ISO: unexpected status %s", resp.Status)
	}

	if err := os.MkdirAll(t.basePath, 0o755); err != nil {
		return err
	}

	f, err := os.Create(t.AbsolutePath())
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := t.CreateImage(f, resp.Body, resp.ContentLength); err != nil {
		return err
	}

	sha256, err := t.checksum()
	if err != nil {
		return err
	}
	if sha256 != targetChecksum {
		t.provider.Logger().Error("checksum mismatch",
			zap.String("file", t.AbsolutePath()),
			zap.String("expected", targetChecksum),
			zap.String("actual", sha256))
		return fmt.Errorf("checksum mismatch: expected %s, got %s", targetChecksum, sha256)
	}

	return nil
}

func (t *TrueNASImage) fetchRemoteChecksum(ctx context.Context, httpClient *http.Client) (string, error) {
	checksumURL := fmt.Sprintf(TrueNASDownloadChecksumURL, t.version, t.version)
	req, err := http.NewRequestWithContext(ctx, "GET", checksumURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching checksum: unexpected status %s from %s", resp.Status, checksumURL)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	// The .sha256 file may be just the hex hash, or "hash  filename" format.
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return "", fmt.Errorf("checksum not found for %s", t.Name())
	}
	return fields[0], nil
}
