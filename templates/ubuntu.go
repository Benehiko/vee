package templates

import (
	"context"

	"github.com/Benehiko/vee/images"
	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/qemu"
)

type Ubuntu struct {
	*qemu.BaseMachine
	*images.UbuntuImage
}

func NewUbuntu(ctx context.Context, provider provider.Provider, version images.UbuntuVersion, imageType images.UbuntuImageType, arch string) (*Ubuntu, error) {
	img := images.NewUbuntuImage(provider, imageType, version, arch)
	if err := img.Download(ctx); err != nil {
		return nil, err
	}
	opts := []qemu.QemuOptions{
		qemu.WithMemory("2G"),
		qemu.WithVGA("virtio"),
	}
	m, err := qemu.NewEmptyMachine(provider)
	if err != nil {
		return nil, err
	}
	iso := qemu.NewDisk(provider, m,
		qemu.WithCustomPath(img.AbsolutePath()),
		qemu.WithMedia(qemu.DiskMediaCdrom),
		qemu.WithInterface(qemu.InterfaceVirtio),
		qemu.WithReadonly(true))

	osDisk := qemu.NewDisk(provider, m,
		qemu.WithSize("20G"),
		qemu.WithFormat(qemu.QCOW2),
		qemu.WithInterface(qemu.InterfaceVirtio),
		qemu.WithCache(qemu.CacheWriteback),
	)
	opts = append(opts, qemu.AddDisk(iso))
	opts = append(opts, qemu.AddDisk(osDisk))

	m, err = m.BuildMachine(opts...)
	if err != nil {
		return nil, err
	}

	return &Ubuntu{
		BaseMachine: m,
		UbuntuImage: img,
	}, nil
}

func NewUbuntuServer24(ctx context.Context, provider provider.Provider) (*Ubuntu, error) {
	return NewUbuntu(ctx, provider, images.Ubuntu2404, images.UbuntuServer, "amd64")
}

func (u *Ubuntu) Start(ctx context.Context) error {
	return u.BaseMachine.Start(ctx)
}
