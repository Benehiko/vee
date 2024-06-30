package qemu

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/utils"
	"go.uber.org/zap"
)

type DiskFormat string

const (
	QCOW2 DiskFormat = "qcow2"
	RAW   DiskFormat = "raw"
	VMDK  DiskFormat = "vmdk"
	VDI   DiskFormat = "vdi"
	VHD   DiskFormat = "vhd"
)

type DiskCache string

const (
	CacheNone         DiskCache = "none"
	CacheWriteback    DiskCache = "writeback"
	CacheUnsafe       DiskCache = "unsafe"
	CacheDirectSync   DiskCache = "directsync"
	CacheWritethrough DiskCache = "writethrough"
)

type DiskInterface string

const (
	InterfaceIDE    DiskInterface = "ide"
	InterfaceSCSI   DiskInterface = "scsi"
	InterfaceSD     DiskInterface = "sd"
	InterfaceMTD    DiskInterface = "mtd"
	InterfaceFloppy DiskInterface = "floppy"
	InterfacePflash DiskInterface = "pflash"
	InterfaceVirtio DiskInterface = "virtio"
	InterfaceNone   DiskInterface = "none"
)

type Disk struct {
	provider provider.Provider

	Machine Machine
	// the absolute path to the disk image
	// can also be a URL to a remote disk (e.g. http://example.com/disk.iso)
	Path string
	// index of the disk
	Index int
	// format of the disk image (qcow2, raw, vmdk, vdi, vhd, iso)
	Format DiskFormat
	// if true the disk is read-only
	Readonly bool
	// media type of the disk "disk" or "cdrom"
	Media DiskMedia
	// interface type of the disk "ide", "scsi", "sd", "mtd", "floppy", "pflash", "virtio", "none"
	Interface DiskInterface
	// cache mode of the disk "none", "writeback", "unsafe", "directsync", "writethrough"
	Cache DiskCache
	// size of the disk image
	// e.g. "10G", "1T"
	Size string
	// recreate the disk image if it already exists
	Recreate bool
}

var _ Builder = &Disk{}

type DiskOptions func(*Disk)

type DiskMedia string

const (
	DiskMediaDisk  DiskMedia = "disk"
	DiskMediaCdrom DiskMedia = "cdrom"
)

func WithRecreate(recreate bool) DiskOptions {
	return func(disk *Disk) {
		disk.Recreate = recreate
	}
}

func WithSize(size string) DiskOptions {
	return func(disk *Disk) {
		disk.Size = size
	}
}

func WithCustomPath(path string) DiskOptions {
	return func(disk *Disk) {
		disk.Path = path
	}
}

func WithMedia(media DiskMedia) DiskOptions {
	return func(disk *Disk) {
		disk.Media = media
	}
}

func WithInterface(iface DiskInterface) DiskOptions {
	return func(disk *Disk) {
		disk.Interface = iface
	}
}

func WithCache(cache DiskCache) DiskOptions {
	return func(disk *Disk) {
		disk.Cache = cache
	}
}

func WithFormat(format DiskFormat) DiskOptions {
	return func(disk *Disk) {
		disk.Format = format
	}
}

func WithReadonly(readonly bool) DiskOptions {
	return func(disk *Disk) {
		disk.Readonly = readonly
	}
}

// NewDisk creates a new Disk with the given path and index
// index is used for boot order
// default format is qcow2
func NewDisk(provider provider.Provider, machine Machine, opts ...DiskOptions) *Disk {
	conf := provider.Config()

	d := &Disk{
		provider:  provider,
		Machine:   machine,
		Path:      filepath.Join(machine.AbsolutePath(), "storage"),
		Size:      conf.DefaultDiskSize,
		Index:     -1,
		Format:    "",
		Readonly:  false,
		Media:     "disk",
		Cache:     CacheNone,
		Interface: InterfaceVirtio,
		Recreate:  false,
	}

	for _, opt := range opts {
		opt(d)
	}

	return d
}

func (q *Disk) Create(ctx context.Context) error {
	if q.Media == DiskMediaCdrom {
		q.provider.Logger().Info("skipping disk creation", zap.String("reason", "iso disk"))
		return nil
	}
	if _, err := exec.LookPath("qemu-img"); err != nil {
		return err
	}
	if q.Recreate {
		if err := q.Delete(); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(q.Path, 0755); err != nil {
		return err
	}

	if _, err := os.Stat(q.AbsolutePath()); err == nil {
		return errors.New("disk already exists")
	}
	cmd := exec.CommandContext(ctx, "qemu-img", "create", "-f", string(q.Format), q.AbsolutePath(), q.Size)
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer
	defer writer.Close()

	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			q.provider.Logger().Info(scanner.Text(),
				zap.String("machine", q.Machine.Name()),
				zap.String("disk", q.Name()))
		}
	}()

	return cmd.Run()
}

func (q *Disk) Delete() error {
	if q.Media == DiskMediaCdrom {
		return errors.New("iso disks should be deleted by the images package")
	}
	if _, err := os.Stat(q.AbsolutePath()); err != nil {
		return nil
	}
	return os.Remove(q.AbsolutePath())
}

func (q *Disk) AbsolutePath() string {
	suffixes := []string{"qcow", "raw", "iso", "vmdk", "vdi", "vhd"}
	for _, suffix := range suffixes {
		if strings.HasSuffix(q.Path, string(suffix)) {
			return q.Path
		}
	}
	return filepath.Join(q.Path, q.Name())
}

func (q *Disk) Name() string {
	return fmt.Sprintf("disk-%s-%s.%s", q.Machine.Name(), q.Size, q.Format)
}

func (q *Disk) FixOptions() {
	if q.Media == DiskMediaCdrom {
		if !q.Readonly {
			q.provider.Logger().Warn("cdrom disk is always readonly", zap.String("disk", q.Name()))
		}
		q.Readonly = true

		if string(q.Format) != "" {
			q.provider.Logger().Warn("cannot set format for cdrom disk", zap.String("disk", q.Name()))
		}
		q.Format = DiskFormat("")
		if q.Cache != CacheNone {
			q.provider.Logger().Warn("cannot set cache for cdrom disk", zap.String("disk", q.Name()))
		}
		q.Cache = CacheNone
	}
}

func (q *Disk) Args() []string {
	q.FixOptions()

	var args []string
	id := fmt.Sprintf("disk-%s", utils.GeneratePetname())

	driveArgs := []string{
		"file=" + q.AbsolutePath(),
		"readonly=" + fmt.Sprintf("%t", q.Readonly),
		"media=" + string(q.Media),
		"if=" + string(q.Interface),
		"id=" + id,
	}
	if q.Index > 0 {
		driveArgs = append(driveArgs, fmt.Sprintf("index=%d", q.Index))
	}
	if q.Media == DiskMediaDisk {
		driveArgs = append(driveArgs, "format="+string(q.Format))
	}
	return append(args, "-drive", strings.Join(driveArgs, ","))
}
