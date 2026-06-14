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
	UbuntuDownloadChecksumURL = "https://releases.ubuntu.com/%s/SHA256SUMS"

	// UbuntuCloudImageURL is the base URL for pre-installed cloud images.
	// Args: version, version, arch ("amd64" or "arm64").
	UbuntuCloudImageURL         = "https://cloud-images.ubuntu.com/releases/%s/release/ubuntu-%s-server-cloudimg-%s.img"
	UbuntuCloudImageChecksumURL = "https://cloud-images.ubuntu.com/releases/%s/release/SHA256SUMS"
)

type UbuntuImageType string

const (
	UbuntuDesktop UbuntuImageType = "desktop"
	UbuntuServer  UbuntuImageType = "live-server"
)

type UbuntuVersion string

// Ubuntu versions are the release "major.minor" (e.g. "24.04"). Ubuntu's
// download layout keys both the cloud-image directory and the installer ISO's
// SHA256SUMS index on major.minor; the installer ISO filename additionally
// embeds a point release (e.g. 24.04.3), which is resolved at download time.
const (
	Ubuntu2004 UbuntuVersion = "20.04"
	Ubuntu2204 UbuntuVersion = "22.04"
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

	// resolvedName is the exact ISO filename discovered from SHA256SUMS at
	// download time (it embeds a point release, e.g. "24.04.3"). Empty until
	// resolved; Name() falls back to a stable placeholder.
	resolvedName string
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

// nameSuffix is the trailing "<type>-<arch>.iso" that identifies this image's
// line in SHA256SUMS regardless of the point-release prefix.
func (u *UbuntuImage) nameSuffix() string {
	return "-" + string(u.imageType) + "-" + u.arch + ".iso"
}

func (u *UbuntuImage) Name() string {
	if u.resolvedName != "" {
		return u.resolvedName
	}
	return "ubuntu-" + string(u.version) + string(u.nameSuffix())
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
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ubuntu: fetch checksums %s: HTTP %d", checksumURL, resp.StatusCode)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// SHA256SUMS lines look like: "<sha256> *ubuntu-24.04.3-live-server-amd64.iso".
	// The filename embeds a point release that isn't known ahead of time, so
	// match by the "-<type>-<arch>.iso" suffix and pick the newest (lexically
	// greatest, which for zero-padded point releases is the latest) entry.
	suffix := u.nameSuffix()
	var targetChecksum string
	for c := range strings.SplitSeq(string(b), "\n") {
		fields := strings.Fields(c)
		if len(fields) != 2 {
			continue
		}
		fname := strings.TrimPrefix(fields[1], "*")
		if !strings.HasPrefix(fname, "ubuntu-"+string(u.version)) || !strings.HasSuffix(fname, suffix) {
			continue
		}
		if fname > u.resolvedName {
			u.resolvedName = fname
			targetChecksum = fields[0]
		}
	}

	if u.resolvedName == "" || targetChecksum == "" {
		u.provider.Logger().Error("checksum not found", zap.String("file", u.Name()))
		return fmt.Errorf("no %s ISO found for ubuntu %s in %s", u.imageType, u.version, checksumURL)
	}

	// Re-check the cache now that the exact filename is known.
	if _, err := os.Stat(u.AbsolutePath()); err == nil {
		u.provider.Logger().Info("skipping download",
			zap.String("file", u.AbsolutePath()),
			zap.String("reason", "already downloaded"))
		return nil
	}

	isoURL := fmt.Sprintf("https://releases.ubuntu.com/%s/%s", u.version, u.resolvedName)
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
		return fmt.Errorf("ubuntu: fetch ISO %s: HTTP %d", isoURL, resp.StatusCode)
	}

	if err := os.MkdirAll(u.basePath, 0o750); err != nil {
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
	// arch is the Ubuntu image arch slug ("amd64" or "arm64"), matching the Go
	// GOARCH naming so platform.HostArch() can be passed through directly.
	arch string
}

func NewUbuntuCloudImage(p provider.Provider, version UbuntuVersion, arch string) *UbuntuCloudImage {
	if arch == "" {
		arch = "amd64"
	}
	return &UbuntuCloudImage{
		BaseImage: NewBaseImage(p),
		version:   version,
		arch:      arch,
	}
}

func (u *UbuntuCloudImage) Distro() string  { return "ubuntu" }
func (u *UbuntuCloudImage) Version() string { return string(u.version) }

func (u *UbuntuCloudImage) Name() string {
	return fmt.Sprintf("ubuntu-%s-server-cloudimg-%s.img", u.version, u.arch)
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

	imgURL := fmt.Sprintf(UbuntuCloudImageURL, u.version, u.version, u.arch)
	req, err = http.NewRequestWithContext(ctx, "GET", imgURL, nil)
	if err != nil {
		return err
	}
	resp, err = httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if err := os.MkdirAll(u.basePath, 0o750); err != nil {
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
