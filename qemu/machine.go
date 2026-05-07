package qemu

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

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
	display      string
	headless     bool
	extraDevices []string
	disks        []*Disk
	virtiofsd    *Virtiofsd
	spice        *Spice
	nics         []*NIC
	uefi         *UEFI
	qmpSocket    string
	memfd        *MemfdBackend
	vfioDevices  []*VFIODevice
	tpm          *TPM
	vsock        *VSockDevice
}

var (
	_ Builder = &BaseMachine{}
	_ Machine = &BaseMachine{}
)

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

func WithNIC(nic *NIC) QemuOptions {
	return func(q *BaseMachine) {
		q.nics = append(q.nics, nic)
	}
}

func WithUEFI(uefi *UEFI) QemuOptions {
	return func(q *BaseMachine) {
		q.uefi = uefi
	}
}

func WithQMPSocket(path string) QemuOptions {
	return func(q *BaseMachine) {
		q.qmpSocket = path
	}
}

func WithMemfd(memfd *MemfdBackend) QemuOptions {
	return func(q *BaseMachine) {
		q.memfd = memfd
	}
}

func WithSpice(spice *Spice) QemuOptions {
	return func(q *BaseMachine) {
		q.spice = spice
	}
}

func WithVFIO(dev *VFIODevice) QemuOptions {
	return func(q *BaseMachine) {
		q.vfioDevices = append(q.vfioDevices, dev)
	}
}

// WithDevice appends a raw -device argument (e.g. "virtio-vga-gl").
func WithDevice(device string) QemuOptions {
	return func(q *BaseMachine) {
		q.extraDevices = append(q.extraDevices, device)
	}
}

// WithDisplay sets the -display argument (e.g. "gtk,gl=on").
func WithDisplay(display string) QemuOptions {
	return func(q *BaseMachine) {
		q.display = display
	}
}

// WithHeadless disables all graphical output (-display none -nographic).
func WithHeadless() QemuOptions {
	return func(q *BaseMachine) {
		q.headless = true
	}
}

func WithTPM(tpm *TPM) QemuOptions {
	return func(q *BaseMachine) {
		q.tpm = tpm
	}
}

func WithVSock(dev *VSockDevice) QemuOptions {
	return func(q *BaseMachine) {
		q.vsock = dev
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

	if q.memfd != nil {
		args = append(args, q.memfd.Args()...)
	}

	args = append(args, q.cpu.Args()...)

	if q.uefi != nil {
		args = append(args, q.uefi.Args()...)
	}

	for i, disk := range q.disks {
		disk.diskIndex = i
		args = append(args, disk.Args()...)
	}

	if q.vga != "" {
		args = append(args, "-vga", string(q.vga))
	}

	for _, nic := range q.nics {
		args = append(args, nic.Args()...)
	}

	if q.virtiofsd != nil {
		args = append(args, q.virtiofsd.Args()...)
	}

	if q.spice != nil {
		args = append(args, q.spice.Args()...)
	}

	for _, vfio := range q.vfioDevices {
		args = append(args, vfio.Args()...)
	}

	if q.tpm != nil {
		args = append(args, q.tpm.Args()...)
	}

	if q.vsock != nil {
		args = append(args, q.vsock.Args()...)
	}

	for _, dev := range q.extraDevices {
		args = append(args, "-device", dev)
	}

	if q.headless {
		args = append(args, "-display", "none", "-nographic")
	} else if q.display != "" {
		args = append(args, "-display", q.display)
	}

	if q.qmpSocket != "" {
		args = append(args, "-qmp", fmt.Sprintf("unix:%s,server,nowait", q.qmpSocket))
	}

	return args
}

// StartResult holds the outcome of a detached VM start.
type StartResult struct {
	PID       int
	QMPSocket string
}

// Start runs QEMU in the foreground (blocks until the VM exits).
func (q *BaseMachine) Start(ctx context.Context) error {
	if _, err := q.start(ctx, false); err != nil {
		return err
	}
	return nil
}

// StartDetached launches QEMU as a detached process (survives terminal close).
// It polls the QMP socket to confirm QEMU is alive before returning.
func (q *BaseMachine) StartDetached(ctx context.Context) (*StartResult, error) {
	return q.start(ctx, true)
}

func (q *BaseMachine) start(ctx context.Context, detach bool) (*StartResult, error) {
	for _, disk := range q.disks {
		if err := disk.Create(ctx); err != nil {
			return nil, err
		}
	}

	binary := q.provider.Config().QemuBinaryPath
	args := q.Args()
	q.provider.Logger().Info("starting QEMU",
		zap.String("machine", q.name),
		zap.String("binary", binary),
		zap.Strings("args", args))

	cmd := exec.CommandContext(ctx, binary, args...)

	if detach {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		// Redirect output to a log file so it is not lost.
		logPath := filepath.Join(q.AbsolutePath(), "qemu.log")
		if err := os.MkdirAll(q.AbsolutePath(), 0o755); err != nil {
			return nil, err
		}
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, err
		}
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		if err := cmd.Start(); err != nil {
			_ = logFile.Close()
			return nil, err
		}
		_ = logFile.Close()

		pid := cmd.Process.Pid

		// If a QMP socket was configured, wait for it to appear.
		if q.qmpSocket != "" {
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(q.qmpSocket); err == nil {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
		}

		return &StartResult{PID: pid, QMPSocket: q.qmpSocket}, nil
	}

	// Foreground mode: pipe output through the logger.
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer
	defer func() { _ = writer.Close() }()

	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			q.provider.Logger().Info(scanner.Text(), zap.String("machine", q.name))
		}
	}()

	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return &StartResult{PID: 0}, nil
}

func (q *BaseMachine) AbsolutePath() string {
	return filepath.Join(q.basePath, q.name)
}

func (q *BaseMachine) Name() string {
	return q.name
}
