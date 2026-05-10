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
	UbuntuDownloadURL         = "https://releases.ubuntu.com/%s/ubuntu-%s-%s-%s.iso"
	UbuntuDownloadChecksumURL = "https://releases.ubuntu.com/%s/SHA256SUMS"

	// UbuntuCloudImageURL is the base URL for pre-installed cloud images.
	UbuntuCloudImageURL         = "https://cloud-images.ubuntu.com/releases/%s/release/ubuntu-%s-server-cloudimg-amd64.img"
	UbuntuCloudImageChecksumURL = "https://cloud-images.ubuntu.com/releases/%s/release/SHA256SUMS"
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
	Ubuntu2410 UbuntuVersion = "24.10"
)

// KnownUbuntuVersions lists supported Ubuntu versions, newest LTS first.
// 24.10 (oracular) is excluded from the default list — it reached EOL in July 2025
// and its package repos no longer serve a Release file.
var KnownUbuntuVersions = []UbuntuVersion{
	Ubuntu2404,
	Ubuntu2204,
	Ubuntu2004,
}

type UbuntuImage struct {
	*BaseImage
	imageType UbuntuImageType
	version   UbuntuVersion
	arch      string
}

func NewUbuntuImage(p provider.Provider, imageType UbuntuImageType, version UbuntuVersion, arch string) *UbuntuImage {
	baseImage := NewBaseImage(p)
	return &UbuntuImage{
		BaseImage: baseImage,
		imageType: imageType,
		version:   version,
		arch:      arch,
	}
}

func (u *UbuntuImage) Distro() string  { return "ubuntu" }
func (u *UbuntuImage) Version() string { return string(u.version) }

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

	// Skip network entirely if the file already exists — it was verified on
	// first download. Only hit the checksum URL when the file is absent.
	if _, err := os.Stat(u.AbsolutePath()); err == nil {
		u.provider.Logger().Info("skipping download",
			zap.String("file", u.AbsolutePath()),
			zap.String("reason", "already downloaded"))
		return nil
	}

	checksumURL := fmt.Sprintf(UbuntuDownloadChecksumURL, u.version)

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

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var targetChecksum string
	for c := range strings.SplitSeq(string(b), "\n") {
		u.provider.Logger().Info("checksum", zap.String("line", c))
		if strings.Contains(c, u.Name()) {
			checksumParts := strings.Split(c, " ")
			targetChecksum = checksumParts[0]
			break
		}
	}

	if targetChecksum == "" {
		u.provider.Logger().Error("checksum not found", zap.String("file", u.Name()))
		return fmt.Errorf("checksum not found for %s", u.Name())
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

	if err := os.MkdirAll(u.basePath, 0o755); err != nil {
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
	return filepath.Join(u.basePath, u.Name())
}

// UbuntuCloudImage is a pre-installed Ubuntu cloud image (.img) that supports
// standard cloud-init user-data on first boot, unlike the live-server ISO which
// requires subiquity autoinstall format.
type UbuntuCloudImage struct {
	*BaseImage
	version UbuntuVersion
}

func NewUbuntuCloudImage(p provider.Provider, version UbuntuVersion) *UbuntuCloudImage {
	return &UbuntuCloudImage{
		BaseImage: NewBaseImage(p),
		version:   version,
	}
}

func (u *UbuntuCloudImage) Distro() string  { return "ubuntu" }
func (u *UbuntuCloudImage) Version() string { return string(u.version) }

func (u *UbuntuCloudImage) Name() string {
	return fmt.Sprintf("ubuntu-%s-server-cloudimg-amd64.img", u.version)
}

func (u *UbuntuCloudImage) AbsolutePath() string {
	return filepath.Join(u.basePath, u.Name())
}

func (u *UbuntuCloudImage) Delete() error {
	if _, err := os.Stat(u.AbsolutePath()); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(u.AbsolutePath())
}

func (u *UbuntuCloudImage) checksum() (string, error) {
	return checksum.SHA256sum(u.AbsolutePath())
}

func (u *UbuntuCloudImage) Download(ctx context.Context) error {
	u.provider.Logger().Info("downloading", zap.String("file", u.Name()))

	// Skip network entirely if the file already exists — it was verified on
	// first download. Only hit the checksum URL when the file is absent.
	if _, err := os.Stat(u.AbsolutePath()); err == nil {
		u.provider.Logger().Info("skipping download",
			zap.String("file", u.AbsolutePath()),
			zap.String("reason", "already downloaded"))
		return nil
	}

	checksumURL := fmt.Sprintf(UbuntuCloudImageChecksumURL, u.version)
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

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var targetChecksum string
	for line := range strings.SplitSeq(string(b), "\n") {
		if strings.Contains(line, u.Name()) {
			parts := strings.Fields(line)
			if len(parts) >= 1 {
				targetChecksum = parts[0]
			}
			break
		}
	}
	if targetChecksum == "" {
		return fmt.Errorf("checksum not found for %s", u.Name())
	}

	if _, err := os.Stat(u.AbsolutePath()); err == nil {
		sha256, err := u.checksum()
		if err != nil {
			return err
		}
		if sha256 == targetChecksum {
			u.provider.Logger().Info("skipping download",
				zap.String("file", u.AbsolutePath()),
				zap.String("reason", "already downloaded"))
			return nil
		}
		u.provider.Logger().Warn("removing file due to checksum mismatch",
			zap.String("file", u.AbsolutePath()),
			zap.String("expected", targetChecksum),
			zap.String("actual", sha256))
		if err := os.Remove(u.AbsolutePath()); err != nil {
			return err
		}
	}

	imgURL := fmt.Sprintf(UbuntuCloudImageURL, u.version, u.version)
	req, err = http.NewRequestWithContext(ctx, "GET", imgURL, nil)
	if err != nil {
		return err
	}
	resp, err = httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if err := os.MkdirAll(u.basePath, 0o755); err != nil {
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

	sha256, err := u.checksum()
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
