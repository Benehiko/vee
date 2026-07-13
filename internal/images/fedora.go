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

	"github.com/Benehiko/vee/internal/platform"
	"github.com/Benehiko/vee/internal/utils"
	"github.com/Benehiko/vee/provider"
)

// fedoraReleasesBase is the Fedora mirror-redirect base for released images.
const fedoraReleasesBase = "https://download.fedoraproject.org/pub/fedora/linux/releases"

// FedoraVersion is a Fedora release number like "42".
type FedoraVersion string

// KnownFedoraVersions lists supported Fedora versions, newest first. Only
// releases whose cloud image follows the modern "Fedora-Cloud-Base-Generic"
// naming are listed; older releases used a different scheme and are omitted.
var KnownFedoraVersions = []FedoraVersion{
	"42",
	"41",
}

// fedoraCompose maps a Fedora release to its cloud-image "compose" suffix. The
// suffix is part of the published filename (e.g. the "1.1" in
// Fedora-Cloud-Base-Generic-42-1.1.aarch64.qcow2) and is bumped per respin, so
// it must be tracked alongside the release number.
var fedoraCompose = map[FedoraVersion]string{
	"42": "1.1",
	"41": "1.4",
}

// FedoraCloudImage is the pre-installed Fedora Cloud Base qcow2 image. It boots
// with cloud-init via the NoCloud datasource (same as the Ubuntu cloud image),
// so the devbox/server/desktop templates can drive it with standard user-data.
//
// The earlier Server DVD ISO was unusable as a qcow2 backing file (which every
// cloud-init template requires), so this replaces it. The qcow2 is published
// for both aarch64 and x86_64, which is what enables Fedora guests on Apple
// Silicon.
type FedoraCloudImage struct {
	*BaseImage
	version FedoraVersion
	// arch is the Fedora arch slug ("aarch64" or "x86_64").
	arch string
}

// NewFedoraCloudImage builds the cloud image for a release on the host's native
// guest architecture. hostArch is a Go GOARCH value ("arm64"/"amd64"); it is
// mapped to the Fedora arch slug internally.
func NewFedoraCloudImage(p provider.Provider, version FedoraVersion, hostArch string) *FedoraCloudImage {
	return &FedoraCloudImage{
		BaseImage: NewBaseImage(p),
		version:   version,
		arch:      platform.GuestArchForHostArch(hostArch),
	}
}

func (f *FedoraCloudImage) Distro() string  { return "fedora" }
func (f *FedoraCloudImage) Version() string { return string(f.version) }

func (f *FedoraCloudImage) compose() string { return fedoraCompose[f.version] }

func (f *FedoraCloudImage) Name() string {
	return fmt.Sprintf("Fedora-Cloud-Base-Generic-%s-%s.%s.qcow2", f.version, f.compose(), f.arch)
}

func (f *FedoraCloudImage) AbsolutePath() string {
	return filepath.Join(f.basePath, f.Name())
}

func (f *FedoraCloudImage) imageURL() string {
	return fmt.Sprintf("%s/%s/Cloud/%s/images/%s", fedoraReleasesBase, f.version, f.arch, f.Name())
}

func (f *FedoraCloudImage) checksumURL() string {
	return fmt.Sprintf("%s/%s/Cloud/%s/images/Fedora-Cloud-%s-%s-%s-CHECKSUM",
		fedoraReleasesBase, f.version, f.arch, f.version, f.compose(), f.arch)
}

func (f *FedoraCloudImage) Delete() error {
	if _, err := os.Stat(f.AbsolutePath()); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(f.AbsolutePath())
}

func (f *FedoraCloudImage) checksum() (string, error) {
	return checksum.SHA256sum(f.AbsolutePath())
}

func (f *FedoraCloudImage) Download(ctx context.Context) error {
	if _, ok := fedoraCompose[f.version]; !ok {
		return fmt.Errorf("unsupported Fedora version %q (known: %v)", f.version, KnownFedoraVersions)
	}

	f.provider.Logger().Info("downloading", zap.String("file", f.Name()))

	httpClient := utils.DirectHTTPClient()
	req, err := http.NewRequestWithContext(ctx, "GET", f.checksumURL(), nil)
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

	// CHECKSUM file is BSD-style: "SHA256 (filename) = hash".
	var targetChecksum string
	for line := range strings.SplitSeq(string(b), "\n") {
		if strings.Contains(line, f.Name()) && strings.HasPrefix(strings.TrimSpace(line), "SHA256") {
			parts := strings.Split(line, "=")
			if len(parts) == 2 {
				targetChecksum = strings.TrimSpace(parts[1])
			}
			break
		}
	}
	if targetChecksum == "" {
		return fmt.Errorf("checksum not found for %s", f.Name())
	}

	if _, err := os.Stat(f.AbsolutePath()); err == nil {
		sha256, err := f.checksum()
		if err != nil {
			return err
		}
		if sha256 == targetChecksum {
			f.provider.Logger().Info("skipping download",
				zap.String("file", f.AbsolutePath()),
				zap.String("reason", "already downloaded"))
			return nil
		}
		f.provider.Logger().Warn("removing file due to checksum mismatch",
			zap.String("file", f.AbsolutePath()),
			zap.String("expected", targetChecksum),
			zap.String("actual", sha256))
		if err := os.Remove(f.AbsolutePath()); err != nil {
			return err
		}
	}

	req, err = http.NewRequestWithContext(ctx, "GET", f.imageURL(), nil)
	if err != nil {
		return err
	}

	resp, err = httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if err := os.MkdirAll(f.basePath, 0o750); err != nil {
		return err
	}

	out, err := os.Create(f.AbsolutePath())
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	if err := f.CreateImage(out, resp.Body, resp.ContentLength); err != nil {
		return err
	}

	sha256, err := f.checksum()
	if err != nil {
		return err
	}
	if sha256 != targetChecksum {
		f.provider.Logger().Error("checksum mismatch",
			zap.String("file", f.AbsolutePath()),
			zap.String("expected", targetChecksum),
			zap.String("actual", sha256))
		return fmt.Errorf("checksum mismatch: expected %s, got %s", targetChecksum, sha256)
	}

	return nil
}
