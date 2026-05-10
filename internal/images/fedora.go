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
	FedoraDownloadURL         = "https://download.fedoraproject.org/pub/fedora/linux/releases/%s/Server/x86_64/iso/Fedora-Server-dvd-x86_64-%s-1.1.iso"
	FedoraDownloadChecksumURL = "https://download.fedoraproject.org/pub/fedora/linux/releases/%s/Server/x86_64/iso/Fedora-Server-dvd-x86_64-%s-1.1-CHECKSUM"
)

// FedoraVersion is a Fedora release number like "42".
type FedoraVersion string

// KnownFedoraVersions lists supported Fedora versions, newest first.
var KnownFedoraVersions = []FedoraVersion{
	"42",
	"41",
	"40",
}

type FedoraImage struct {
	*BaseImage
	version FedoraVersion
}

func NewFedoraImage(p provider.Provider, version FedoraVersion) *FedoraImage {
	return &FedoraImage{
		BaseImage: NewBaseImage(p),
		version:   version,
	}
}

func (f *FedoraImage) Distro() string  { return "fedora" }
func (f *FedoraImage) Version() string { return string(f.version) }
func (f *FedoraImage) Name() string {
	return fmt.Sprintf("Fedora-Server-dvd-x86_64-%s-1.1.iso", f.version)
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

func (fi *FedoraImage) Download(ctx context.Context) error {
	fi.provider.Logger().Info("downloading", zap.String("file", fi.Name()))

	checksumURL := fmt.Sprintf(FedoraDownloadChecksumURL, fi.version, fi.version)

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

	// CHECKSUM file format: SHA256 (filename) = hash
	var targetChecksum string
	for line := range strings.SplitSeq(string(b), "\n") {
		if strings.Contains(line, fi.Name()) && strings.HasPrefix(strings.TrimSpace(line), "SHA256") {
			parts := strings.Split(line, "=")
			if len(parts) == 2 {
				targetChecksum = strings.TrimSpace(parts[1])
			}
			break
		}
	}

	if targetChecksum == "" {
		return fmt.Errorf("checksum not found for %s", fi.Name())
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

	req, err = http.NewRequestWithContext(ctx, "GET", fmt.Sprintf(FedoraDownloadURL, fi.version, fi.version), nil)
	if err != nil {
		return err
	}

	resp, err = httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

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
