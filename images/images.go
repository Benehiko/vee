package images

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/Benehiko/vee/provider"
	"github.com/schollz/progressbar/v3"
)

type Image interface {
	Name() string
	Delete() error
	Download(context.Context) error
	AbsolutePath() string
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

func NewBaseImage(provider provider.Provider) *BaseImage {
	return &BaseImage{
		provider: provider,
		basePath: filepath.Join(provider.Config().StoragePath, "iso"),
	}
}
