package images

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/Benehiko/vee/provider"
	"github.com/schollz/progressbar/v3"
)

// hrefsIn extracts the values of every href="..." attribute in an HTML
// directory listing. Used to discover the exact ISO/checksum filenames served
// by upstream mirrors instead of hardcoding filenames that drift per release.
func hrefsIn(html string) []string {
	var out []string
	rest := html
	for {
		i := strings.Index(rest, `href="`)
		if i < 0 {
			break
		}
		rest = rest[i+len(`href="`):]
		j := strings.Index(rest, `"`)
		if j < 0 {
			break
		}
		out = append(out, rest[:j])
		rest = rest[j+1:]
	}
	return out
}

type Image interface {
	Name() string
	Delete() error
	Download(context.Context) error
	AbsolutePath() string
	Distro() string
	Version() string
}

type BaseImage struct {
	provider provider.Provider
	basePath string
}

func (b *BaseImage) AbsolutePath() string {
	return b.basePath
}

func (b *BaseImage) CreateImage(f *os.File, src io.Reader, contentLength int64) error {
	bar := progressbar.DefaultBytes(
		contentLength,
		"downloading",
	)

	if _, err := io.Copy(io.MultiWriter(f, bar), src); err != nil {
		return err
	}
	return nil
}

func NewBaseImage(p provider.Provider) *BaseImage {
	return &BaseImage{
		provider: p,
		basePath: p.Config().ISOCachePath,
	}
}
