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
	// Omarchy publishes ISOs on its own mirror (linked from omarchy.org),
	// with a matching "<iso>.sha256" sidecar ("<sha256>  <filename>").
	OmarchyDownloadURL         = "https://iso.omarchy.org/omarchy-%s.iso"
	OmarchyDownloadChecksumURL = "https://iso.omarchy.org/omarchy-%s.iso.sha256"
)

// OmarchyVersion is an Omarchy release string like "4.0.1".
type OmarchyVersion string

// KnownOmarchyVersions lists supported Omarchy releases, newest first.
// The mirror keeps only the current release — older ISOs and their sidecars
// are removed when a new version ships — so this list is refreshed
// periodically; requesting a purged release fails cleanly with an HTTP 404
// on the checksum fetch. "vee pull omarchy latest" resolves to the first entry.
var KnownOmarchyVersions = []OmarchyVersion{
	"4.0.1",
}

type OmarchyImage struct {
	*BaseImage
	version OmarchyVersion
}

func NewOmarchyImage(p provider.Provider, version OmarchyVersion) *OmarchyImage {
	return &OmarchyImage{
		BaseImage: NewBaseImage(p),
		version:   version,
	}
}

func (o *OmarchyImage) Distro() string  { return "omarchy" }
func (o *OmarchyImage) Version() string { return string(o.version) }
func (o *OmarchyImage) Name() string {
	return fmt.Sprintf("omarchy-%s.iso", o.version)
}

func (o *OmarchyImage) AbsolutePath() string {
	return filepath.Join(o.basePath, o.Name())
}

func (o *OmarchyImage) Delete() error {
	if _, err := os.Stat(o.AbsolutePath()); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(o.AbsolutePath())
}

func (o *OmarchyImage) checksum() (string, error) {
	return checksum.SHA256sum(o.AbsolutePath())
}

func (o *OmarchyImage) Download(ctx context.Context) error {
	o.provider.Logger().Info("downloading", zap.String("file", o.Name()))

	checksumURL := fmt.Sprintf(OmarchyDownloadChecksumURL, o.version)

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
		return fmt.Errorf("omarchy: fetch checksum %s: HTTP %d", checksumURL, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// Sidecar format: "<sha256>  <filename>".
	var targetChecksum string
	for line := range strings.SplitSeq(string(body), "\n") {
		if strings.Contains(line, o.Name()) {
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
		return fmt.Errorf("checksum not found for %s", o.Name())
	}

	if _, err := os.Stat(o.AbsolutePath()); err == nil {
		sha256, err := o.checksum()
		if err != nil {
			return err
		}
		if sha256 == targetChecksum {
			o.provider.Logger().Info("skipping download",
				zap.String("file", o.AbsolutePath()),
				zap.String("reason", "already downloaded"))
			return nil
		}
		o.provider.Logger().Warn("removing file due to checksum mismatch",
			zap.String("file", o.AbsolutePath()),
			zap.String("expected", targetChecksum),
			zap.String("actual", sha256))
		if err := os.Remove(o.AbsolutePath()); err != nil {
			return err
		}
	}

	isoURL := fmt.Sprintf(OmarchyDownloadURL, o.version)
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
		return fmt.Errorf("omarchy: fetch ISO %s: HTTP %d", isoURL, resp.StatusCode)
	}

	if err := os.MkdirAll(o.basePath, 0o750); err != nil {
		return err
	}

	f, err := os.Create(o.AbsolutePath())
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	if err := o.CreateImage(f, resp.Body, resp.ContentLength); err != nil {
		return err
	}

	sha256, err := o.checksum()
	if err != nil {
		return err
	}
	if sha256 != targetChecksum {
		o.provider.Logger().Error("checksum mismatch",
			zap.String("file", o.AbsolutePath()),
			zap.String("expected", targetChecksum),
			zap.String("actual", sha256))
		return fmt.Errorf("checksum mismatch: expected %s, got %s", targetChecksum, sha256)
	}

	return nil
}
