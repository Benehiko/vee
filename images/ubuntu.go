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
	UbuntuDownloadURL         = "https://releases.ubuntu.com/%s/ubuntu-%s-%s-%s.iso"
	UbuntuDownloadChecksumURL = "https://releases.ubuntu.com/%s/SHA256SUMS"
)

type UbuntuImageType string

const (
	UbuntuDesktop UbuntuImageType = "desktop"
	UbuntuServer  UbuntuImageType = "live-server"
)

type UbuntuVersion string

const (
	Ubuntu2004 UbuntuVersion = "20.04.6"
	Ubuntu2204 UbuntuVersion = "22.04.4"
	Ubuntu2310 UbuntuVersion = "23.10"
	Ubuntu2404 UbuntuVersion = "24.04"
)

type UbuntuImage struct {
	*BaseImage
	imageType UbuntuImageType
	version   UbuntuVersion
	arch      string
}

func NewUbuntuImage(provider provider.Provider, imageType UbuntuImageType, version UbuntuVersion, arch string) *UbuntuImage {
	baseImage := NewBaseImage(provider)
	return &UbuntuImage{
		BaseImage: baseImage,
		imageType: imageType,
		version:   version,
		arch:      arch,
	}
}

func (u *UbuntuImage) Name() string {
	return "ubuntu-" + string(u.version) + "-" + string(u.imageType) + "-" + u.arch + ".iso"
}

func (u *UbuntuImage) Delete() error {
	if _, err := os.Stat(u.AbsolutePath()); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(u.AbsolutePath())
}

func (u *UbuntuImage) Checksum() (string, error) {
	return checksum.SHA256sum(u.AbsolutePath())
}

func (u *UbuntuImage) Download(ctx context.Context) error {
	u.provider.Logger().Info("downloading", zap.String("file", u.Name()))

	checksumURL := fmt.Sprintf(UbuntuDownloadChecksumURL, u.version)

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
	checksums := strings.Split(string(b), "\n")
	for _, checksum := range checksums {
		u.provider.Logger().Info("checksum", zap.String("line", checksum))
		if strings.Contains(checksum, u.Name()) {
			checksumParts := strings.Split(checksum, " ")
			targetChecksum = checksumParts[0]
			break
		}
	}

	if targetChecksum == "" {
		u.provider.Logger().Error("checksum not found", zap.String("file", u.Name()))
		return fmt.Errorf("checksum not found for %s", u.Name())
	}

	if _, err := os.Stat(u.AbsolutePath()); err == nil {
		sha256, err := u.Checksum()
		if err != nil {
			return err
		}

		if sha256 == targetChecksum {
			u.provider.Logger().Info("skipping download",
				zap.String("file", u.AbsolutePath()),
				zap.String("reason", "already downloaded"))
			return nil
		}

		// checksum mismatch - delete the file
		u.provider.Logger().Warn("removing file due to checksum mismatch",
			zap.String("file", u.AbsolutePath()),
			zap.String("expected", targetChecksum),
			zap.String("actual", sha256))
		if err := os.Remove(u.AbsolutePath()); err != nil {
			return err
		}
	}

	req, err = http.NewRequestWithContext(ctx, "GET", fmt.Sprintf(UbuntuDownloadURL, u.version, u.version, u.imageType, u.arch), nil)
	if err != nil {
		return err
	}

	resp, err = httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if err := os.MkdirAll(u.BaseImage.AbsolutePath(), 0o755); err != nil {
		return err
	}

	f, err := os.Create(u.AbsolutePath())
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := u.CreateImage(f, resp.Body, resp.ContentLength); err != nil {
		return err
	}

	sha256, err := u.Checksum()
	if err != nil {
		return err
	}

	if sha256 != targetChecksum {
		u.provider.Logger().Error("checksum mismatch",
			zap.String("file", u.AbsolutePath()),
			zap.String("expected", targetChecksum),
			zap.String("actual", sha256))
		return fmt.Errorf("checksum mismatch: expected %s, got %s", targetChecksum, sha256)
	}

	return nil
}

func (u *UbuntuImage) AbsolutePath() string {
	return filepath.Join(u.BaseImage.AbsolutePath(), u.Name())
}
