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
	// InterfaceAHCI emits an ahci controller + ide-hd device, matching the
	// SATA bus arrangement that TrueNAS (ZFS) expects for its boot pool.
	InterfaceAHCI DiskInterface = "ahci"
)

type Disk struct {
	provider provider.Provider

	Machine Machine
	// the absolute path to the disk image
	// can also be a URL to a remote disk (e.g. http://example.com/disk.iso)
	Path string
	// Index of the disk for boot order
	Index int
	// diskIndex is set by BaseMachine.Args() to generate a deterministic drive ID.
	diskIndex int
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
	// BackingFile is the path to a base image for a qcow2 overlay disk.
	// When set, Create() makes a thin COW overlay instead of a blank image.
	BackingFile string
	// Serial is emitted as serial=<value> on the virtio-blk-pci device.
	// Useful for passthrough disks so ZFS can identify physical drives.
	Serial string
	// Passthrough marks this disk as a raw host block device passthrough.
	// The Path must be a host device (e.g. /dev/disk/by-id/...).
	// Sets format=raw, cache=none, aio=native automatically.
	Passthrough bool
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

func WithBackingFile(path string) DiskOptions {
	return func(disk *Disk) {
		disk.BackingFile = path
	}
}

func WithSerial(serial string) DiskOptions {
	return func(disk *Disk) {
		disk.Serial = serial
	}
}

func WithPassthrough(passthrough bool) DiskOptions {
	return func(disk *Disk) {
		disk.Passthrough = passthrough
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

// detectImageFormat returns the format string ("qcow2", "raw", etc.) of an image
// file by running qemu-img info. Falls back to "raw" on error.
// detectImageFormat returns the format of an image file using qemu-img info.
// It parses the human-readable "file format:" line which appears once at the top level.
func detectImageFormat(path string) (string, error) {
	out, err := exec.Command("qemu-img", "info", path).Output()
	if err != nil {
		return "raw", fmt.Errorf("qemu-img info: %w", err)
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if val, ok := strings.CutPrefix(line, "file format:"); ok {
			return strings.TrimSpace(val), nil
		}
	}
	return "raw", nil
}

func (q *Disk) Create(ctx context.Context) error {
	if q.Media == DiskMediaCdrom {
		q.provider.Logger().Info("skipping disk creation", zap.String("reason", "iso disk"))
		return nil
	}
	if q.Passthrough {
		q.provider.Logger().Info("skipping disk creation", zap.String("reason", "passthrough disk"))
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

	if err := os.MkdirAll(filepath.Dir(q.AbsolutePath()), 0o755); err != nil {
		return err
	}

	if _, err := os.Stat(q.AbsolutePath()); err == nil {
		q.provider.Logger().Info("skipping disk creation", zap.String("reason", "disk already exists"), zap.String("path", q.AbsolutePath()))
		return nil
	}

	var cmd *exec.Cmd
	if q.BackingFile != "" {
		// Detect the backing file format so the overlay is created correctly.
		backingFmt, err := detectImageFormat(q.BackingFile)
		if err != nil {
			return fmt.Errorf("detect backing file format: %w", err)
		}
		cmd = exec.CommandContext(ctx, "qemu-img", "create",
			"-f", string(q.Format),
			"-b", q.BackingFile,
			"-F", backingFmt,
			q.AbsolutePath(),
			q.Size,
		)
	} else {
		cmd = exec.CommandContext(ctx, "qemu-img", "create", "-f", string(q.Format), q.AbsolutePath(), q.Size)
	}
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer
	defer func() { _ = writer.Close() }()

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
	if q.Passthrough {
		return q.Path
	}
	suffixes := []string{"qcow2", "qcow", "img", "raw", "iso", "vmdk", "vdi", "vhd"}
	for _, suffix := range suffixes {
		if strings.HasSuffix(q.Path, suffix) {
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

	id := fmt.Sprintf("disk%d", q.diskIndex)

	// AHCI disks: emit -device ahci controller (once, tracked externally by
	// the machine) + -drive if=none + -device ide-hd,bus=ahci0.<slot>.
	// The ahci controller itself is added by BaseMachine.Args() before disks.
	if q.Interface == InterfaceAHCI {
		driveArgs := []string{
			"file=" + q.AbsolutePath(),
			"if=none",
			"id=" + id,
			"format=" + string(q.Format),
			"cache=" + string(q.Cache),
			"aio=native",
			"discard=unmap",
		}
		deviceArgs := []string{
			"ide-hd",
			"drive=" + id,
			fmt.Sprintf("bus=ahci0.%d", q.diskIndex),
		}
		return []string{
			"-drive", strings.Join(driveArgs, ","),
			"-device", strings.Join(deviceArgs, ","),
		}
	}

	// Passthrough disks: raw host device via virtio-blk-pci with optional serial.
	if q.Passthrough {
		driveArgs := []string{
			"file=" + q.AbsolutePath(),
			"format=raw",
			"if=none",
			"id=" + id,
			"cache=none",
			"aio=native",
		}
		deviceArgs := []string{
			"virtio-blk-pci",
			"drive=" + id,
		}
		if q.Serial != "" {
			deviceArgs = append(deviceArgs, "serial="+q.Serial)
		}
		return []string{
			"-drive", strings.Join(driveArgs, ","),
			"-device", strings.Join(deviceArgs, ","),
		}
	}

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
		if q.Interface == InterfaceVirtio {
			cache := q.Cache
			if cache == "" {
				cache = CacheNone
			}
			driveArgs = append(driveArgs,
				"cache="+string(cache),
				"aio=io_uring",
				"discard=unmap",
			)
		} else if q.Cache != "" {
			driveArgs = append(driveArgs, "cache="+string(q.Cache))
		}
	} else if q.Cache != "" {
		driveArgs = append(driveArgs, "cache="+string(q.Cache))
	}
	return []string{"-drive", strings.Join(driveArgs, ",")}
}
