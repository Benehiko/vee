package qemu

import (
	"bufio"
	"context"
	"io"
	"log"
	"os/exec"
	"path/filepath"

	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/utils"
	"go.uber.org/zap"
)

type VGA string

const (
	VGACirrus VGA = "cirrus"
	VGAStd    VGA = "std"
	VGAVMWare VGA = "vmware"
	VGAQXL    VGA = "qxl"
	VGATCX    VGA = "tcx"
	VGACG3    VGA = "cg3"
	VGAVirtio VGA = "virtio"
	VGANone   VGA = "none"
)

type BaseMachine struct {
	provider     provider.Provider
	basePath     string
	name         string
	architecture string
	cpu          *CPU
	machineType  string
	memory       string
	vga          string
	disks        []*Disk
	virtiofsd    *Virtiofsd
	spice        *Spice
}

var _ Builder = &BaseMachine{}
var _ Machine = &BaseMachine{}

type QemuOptions func(*BaseMachine)

func WithName(name string) QemuOptions {
	return func(q *BaseMachine) {
		q.name = name
	}
}

func WithVirtiofsd(socketPath, tag string) QemuOptions {
	return func(q *BaseMachine) {
		q.virtiofsd = &Virtiofsd{
			SocketPath: socketPath,
			Tag:        tag,
		}
	}
}

func WithVGA(vga string) QemuOptions {
	return func(q *BaseMachine) {
		q.vga = vga
	}
}

func AddDisk(disk *Disk) QemuOptions {
	return func(q *BaseMachine) {
		q.disks = append(q.disks, disk)
	}
}

func WithMemory(memory string) QemuOptions {
	return func(q *BaseMachine) {
		q.memory = memory
	}
}

func WithCPU(cpu *CPU) QemuOptions {
	return func(q *BaseMachine) {
		q.cpu = cpu
	}
}

func NewEmptyMachine(provider provider.Provider) (*BaseMachine, error) {
	return &BaseMachine{
		provider:     provider,
		basePath:     provider.Config().StoragePath,
		name:         utils.GeneratePetname(),
		memory:       provider.Config().DefaultMemory,
		machineType:  provider.Config().DefaultMachineType,
		architecture: "x86_64",
		cpu:          NewCPU(provider),
	}, nil
}

func (q *BaseMachine) BuildMachine(opts ...QemuOptions) (*BaseMachine, error) {
	for _, opt := range opts {
		opt(q)
	}
	if err := q.Validate(); err != nil {
		return nil, err
	}
	return q, nil
}

func (q *BaseMachine) Validate() error {
	if len(q.disks) == 0 {
		return ErrNoDisks
	}
	return nil
}

func (q *BaseMachine) Args() []string {
	var args []string
	args = append(args, "-machine", q.machineType)
	args = append(args, "-m", q.memory)

	args = append(args, q.cpu.Args()...)

	for _, disk := range q.disks {
		args = append(args, disk.Args()...)
	}

	if q.vga != "" {
		args = append(args, "-vga", string(q.vga))
	}

	if q.virtiofsd != nil {
		args = append(args, q.virtiofsd.Args()...)
	}

	if q.spice != nil {
		args = append(args, q.spice.Args()...)
	}

	return args
}

func (q *BaseMachine) Start(ctx context.Context) error {
	for _, disk := range q.disks {
		if err := disk.Create(ctx); err != nil {
			return err
		}
	}

	// qemu-system-x86_64 -m 2G -drive file=debian.qcow2,format=qcow2 -device virtiofsd-pci,chardev=chardev0,tag=tag0 -chardev socket,id=chardev0,path=/path/to/socket
	args := q.Args()
	log.Printf("qemu-system-x86_64 %v", args)
	cmd := exec.CommandContext(ctx, "qemu-system-x86_64", args...)
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer
	defer writer.Close()

	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			q.provider.Logger().Info(scanner.Text(), zap.String("machine", q.name))
		}
	}()

	return cmd.Run()
}

func (q *BaseMachine) AbsolutePath() string {
	return filepath.Join(q.basePath, q.name)
}

func (q *BaseMachine) Name() string {
	return q.name
}
