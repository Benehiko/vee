package images

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/codingsince1985/checksum"
	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/utils"
	"github.com/Benehiko/vee/provider"
)

const (
	// Bazzite publishes ISOs on its own mirror (not GitHub release assets),
	// with a matching "<iso>-CHECKSUM" sidecar.
	BazziteDownloadURL         = "https://download.bazzite.gg/bazzite-%s-amd64.iso"
	BazziteDownloadChecksumURL = "https://download.bazzite.gg/bazzite-%s-amd64.iso-CHECKSUM"
)

// BazziteVersion is a Bazzite channel like "stable".
type BazziteVersion string

// KnownBazziteVersions lists supported Bazzite channels, newest first.
var KnownBazziteVersions = []BazziteVersion{
	"stable",
}

type BazziteImage struct {
	*BaseImage
	version BazziteVersion
}

func NewBazziteImage(p provider.Provider, version BazziteVersion) *BazziteImage {
	return &BazziteImage{
		BaseImage: NewBaseImage(p),
		version:   version,
	}
}

func (b *BazziteImage) Distro() string  { return "bazzite" }
func (b *BazziteImage) Version() string { return string(b.version) }
func (b *BazziteImage) Name() string {
	return fmt.Sprintf("bazzite-%s-amd64.iso", b.version)
}

func (b *BazziteImage) AbsolutePath() string {
	return filepath.Join(b.basePath, b.Name())
}

func (b *BazziteImage) Delete() error {
	if _, err := os.Stat(b.AbsolutePath()); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(b.AbsolutePath())
}

func (b *BazziteImage) checksum() (string, error) {
	return checksum.SHA256sum(b.AbsolutePath())
}

func (b *BazziteImage) Download(ctx context.Context) error {
	b.provider.Logger().Info("downloading", zap.String("file", b.Name()))

	checksumURL := fmt.Sprintf(BazziteDownloadChecksumURL, b.version)

	httpClient := utils.DirectHTTPClient()
	req, err := http.NewRequestWithContext(ctx, "GET", checksumURL, nil)
	if err != nil {
		return err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bazzite: fetch checksum %s: HTTP %d", checksumURL, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// CHECKSUM format: "<sha256>  <filename>".
	var targetChecksum string
	for line := range strings.SplitSeq(string(body), "\n") {
		if strings.Contains(line, b.Name()) {
			parts := strings.Fields(line)
			if len(parts) >= 1 {
				targetChecksum = parts[0]
			}
			break
		}
	}
	// Some sidecars contain only the bare hash.
	if targetChecksum == "" {
		if fields := strings.Fields(string(body)); len(fields) == 1 && len(fields[0]) == 64 {
			targetChecksum = fields[0]
		}
	}
	if targetChecksum == "" {
		return fmt.Errorf("checksum not found for %s", b.Name())
	}

	if _, err := os.Stat(b.AbsolutePath()); err == nil {
		sha256, err := b.checksum()
		if err != nil {
			return err
		}
		if sha256 == targetChecksum {
			b.provider.Logger().Info("skipping download",
				zap.String("file", b.AbsolutePath()),
				zap.String("reason", "already downloaded"))
			return nil
		}
		b.provider.Logger().Warn("removing file due to checksum mismatch",
			zap.String("file", b.AbsolutePath()),
			zap.String("expected", targetChecksum),
			zap.String("actual", sha256))
		if err := os.Remove(b.AbsolutePath()); err != nil {
			return err
		}
	}

	isoURL := fmt.Sprintf(BazziteDownloadURL, b.version)
	req, err = http.NewRequestWithContext(ctx, "GET", isoURL, nil)
	if err != nil {
		return err
	}

	resp, err = httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bazzite: fetch ISO %s: HTTP %d", isoURL, resp.StatusCode)
	}

	if err := os.MkdirAll(b.basePath, 0o750); err != nil {
		return err
	}

	f, err := os.Create(b.AbsolutePath())
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := b.CreateImage(f, resp.Body, resp.ContentLength); err != nil {
		return err
	}

	sha256, err := b.checksum()
	if err != nil {
		return err
	}
	if sha256 != targetChecksum {
		b.provider.Logger().Error("checksum mismatch",
			zap.String("file", b.AbsolutePath()),
			zap.String("expected", targetChecksum),
			zap.String("actual", sha256))
		return fmt.Errorf("checksum mismatch: expected %s, got %s", targetChecksum, sha256)
	}

	return nil
}
