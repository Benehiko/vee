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
	ArchDownloadURL         = "https://geo.mirror.pkgbuild.com/iso/%s/archlinux-%s-x86_64.iso"
	ArchDownloadChecksumURL = "https://geo.mirror.pkgbuild.com/iso/%s/sha256sums.txt"
)

// ArchVersion is a monthly release string like "2025.05.01".
type ArchVersion string

// KnownArchVersions lists supported release strings, newest first.
var KnownArchVersions = []ArchVersion{
	"2026.05.01",
	"2026.04.01",
	"2026.03.01",
}

type ArchImage struct {
	*BaseImage
	version ArchVersion
}

func NewArchImage(p provider.Provider, version ArchVersion) *ArchImage {
	return &ArchImage{
		BaseImage: NewBaseImage(p),
		version:   version,
	}
}

func (a *ArchImage) Distro() string  { return "arch" }
func (a *ArchImage) Version() string { return string(a.version) }
func (a *ArchImage) Name() string    { return "archlinux-" + string(a.version) + "-x86_64.iso" }

func (a *ArchImage) AbsolutePath() string {
	return filepath.Join(a.basePath, a.Name())
}

func (a *ArchImage) Delete() error {
	if _, err := os.Stat(a.AbsolutePath()); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(a.AbsolutePath())
}

func (a *ArchImage) checksum() (string, error) {
	return checksum.SHA256sum(a.AbsolutePath())
}

func (a *ArchImage) Download(ctx context.Context) error {
	a.provider.Logger().Info("downloading", zap.String("file", a.Name()))

	checksumURL := fmt.Sprintf(ArchDownloadChecksumURL, a.version)

	httpClient := &http.Client{}
	req, err := http.NewRequestWithContext(ctx, "GET", checksumURL, nil)
	if err != nil {
		return err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var targetChecksum string
	for line := range strings.SplitSeq(string(b), "\n") {
		if strings.Contains(line, a.Name()) {
			parts := strings.Fields(line)
			if len(parts) >= 1 {
				targetChecksum = parts[0]
			}
			break
		}
	}

	if targetChecksum == "" {
		return fmt.Errorf("checksum not found for %s", a.Name())
	}

	if _, err := os.Stat(a.AbsolutePath()); err == nil {
		sha256, err := a.checksum()
		if err != nil {
			return err
		}
		if sha256 == targetChecksum {
			a.provider.Logger().Info("skipping download",
				zap.String("file", a.AbsolutePath()),
				zap.String("reason", "already downloaded"))
			return nil
		}
		a.provider.Logger().Warn("removing file due to checksum mismatch",
			zap.String("file", a.AbsolutePath()),
			zap.String("expected", targetChecksum),
			zap.String("actual", sha256))
		if err := os.Remove(a.AbsolutePath()); err != nil {
			return err
		}
	}

	req, err = http.NewRequestWithContext(ctx, "GET", fmt.Sprintf(ArchDownloadURL, a.version, a.version), nil)
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

	f, err := os.Create(a.AbsolutePath())
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := a.CreateImage(f, resp.Body, resp.ContentLength); err != nil {
		return err
	}

	sha256, err := a.checksum()
	if err != nil {
		return err
	}
	if sha256 != targetChecksum {
		a.provider.Logger().Error("checksum mismatch",
			zap.String("file", a.AbsolutePath()),
			zap.String("expected", targetChecksum),
			zap.String("actual", sha256))
		return fmt.Errorf("checksum mismatch: expected %s, got %s", targetChecksum, sha256)
	}

	return nil
}
