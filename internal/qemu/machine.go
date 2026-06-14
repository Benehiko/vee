package qemu

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/platform"
	"github.com/Benehiko/vee/internal/utils"
	"github.com/Benehiko/vee/provider"
)

// Accelerator selects the QEMU acceleration backend emitted as -accel.
type Accelerator string

const (
	// AccelKVM is the Linux Kernel-based Virtual Machine accelerator.
	AccelKVM Accelerator = "kvm"
	// AccelHVF is the macOS Hypervisor.framework accelerator.
	AccelHVF Accelerator = "hvf"
	// AccelTCG is the pure-software Tiny Code Generator (no hardware accel).
	AccelTCG Accelerator = "tcg"
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
	accelerator  Accelerator
	cpu          *CPU
	machineType  string
	memory       string
	vga          string
	display      string
	headless     bool
	extraDevices []string
	disks        []*Disk
	virtiofsd    []*Virtiofsd
	spice        *Spice
	nics         []*NIC
	uefi         *UEFI
	qmpSocket    string
	qgaSocket    string
	memfd        *MemfdBackend
	vfioDevices  []*VFIODevice
	tpm          *TPM
	vsock        *VSockDevice
	cpuPinning   []int    // host CPU indices; empty = no pinning
	rtc          string   // e.g. "base=localtime,clock=host"
	bootOrder    string   // e.g. "c" for disk-first; empty = firmware default
	globals      []string // extra -global args, e.g. "driver=cfi.pflash01,property=secure,value=on"
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

// WithMachineType overrides the -machine type string (e.g. "q35,smm=on").
// Empty leaves the provider default in place.
func WithMachineType(machineType string) QemuOptions {
	return func(q *BaseMachine) {
		if machineType != "" {
			q.machineType = machineType
		}
	}
}

// WithGlobal appends a -global argument (the value after "-global"), e.g.
// "driver=cfi.pflash01,property=secure,value=on" to arm SMM-based Secure Boot.
func WithGlobal(global string) QemuOptions {
	return func(q *BaseMachine) {
		q.globals = append(q.globals, global)
	}
}

func WithVirtiofsd(socketPath, tag string) QemuOptions {
	return func(q *BaseMachine) {
		q.virtiofsd = append(q.virtiofsd, &Virtiofsd{
			SocketPath: socketPath,
			Tag:        tag,
			Chardev:    "virtiofs-" + tag,
			Device:     "vhost-user-fs-pci",
		})
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

// WithAccelerator overrides the acceleration backend (e.g. AccelHVF on macOS,
// AccelKVM on Linux). Empty leaves the host-derived default chosen in
// NewEmptyMachine.
func WithAccelerator(accel Accelerator) QemuOptions {
	return func(q *BaseMachine) {
		q.accelerator = accel
	}
}

// WithArchitecture overrides the guest architecture (e.g. "aarch64", "x86_64").
func WithArchitecture(arch string) QemuOptions {
	return func(q *BaseMachine) {
		q.architecture = arch
	}
}

// WithMachineType overrides the QEMU machine type (e.g. "virt", "q35",
// "vmapple"). Empty leaves the provider default chosen in NewEmptyMachine.
func WithMachineType(machineType string) QemuOptions {
	return func(q *BaseMachine) {
		if machineType != "" {
			q.machineType = machineType
		}
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

func WithQGASocket(path string) QemuOptions {
	return func(q *BaseMachine) {
		q.qgaSocket = path
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

// WithBootOrder sets the QEMU -boot order (e.g. "c" for disk-first).
func WithBootOrder(order string) QemuOptions {
	return func(q *BaseMachine) {
		q.bootOrder = order
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

// WithCPUPinning sets the host CPU indices that vCPU threads will be pinned to
// after QEMU starts. Empty slice disables pinning.
func WithCPUPinning(cpus []int) QemuOptions {
	return func(q *BaseMachine) {
		q.cpuPinning = cpus
	}
}

// WithRTC sets the -rtc argument (e.g. "base=localtime,clock=host").
// Use for Windows/gaming VMs that need the RTC to match local wall-clock time.
func WithRTC(rtc string) QemuOptions {
	return func(q *BaseMachine) {
		q.rtc = rtc
	}
}

func NewEmptyMachine(provider provider.Provider) (*BaseMachine, error) {
	return &BaseMachine{
		provider:     provider,
		basePath:     provider.Config().StoragePath,
		name:         utils.GeneratePetname(),
		memory:       provider.Config().DefaultMemory,
		machineType:  provider.Config().DefaultMachineType,
		architecture: platform.DefaultGuestArch(),
		accelerator:  Accelerator(platform.DefaultAccelerator()),
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

// effectiveMachineType augments the base machine type with arch-specific
// options. The aarch64 "virt" board requires an explicit GIC version; under HVF
// on Apple Silicon that is GICv3, and gic-version=max resolves to the host's
// version. The option is only added when not already present so explicit
// machine-type configuration is respected.
func (q *BaseMachine) effectiveMachineType() string {
	mt := q.machineType
	if (q.architecture == "aarch64" || q.architecture == "arm64") &&
		strings.HasPrefix(mt, "virt") && !strings.Contains(mt, "gic-version") {
		mt += ",gic-version=max"
	}
	return mt
}

func (q *BaseMachine) Args() []string {
	var args []string
	args = append(args, "-machine", q.effectiveMachineType())
	for _, g := range q.globals {
		args = append(args, "-global", g)
	}
	if q.accelerator != "" {
		args = append(args, "-accel", string(q.accelerator))
	}
	args = append(args, "-m", q.memory)

	if q.memfd != nil {
		args = append(args, q.memfd.Args()...)
	}

	args = append(args, q.cpu.Args()...)

	if q.uefi != nil {
		args = append(args, q.uefi.Args()...)
	}

	// Emit AHCI controller once if any disk uses InterfaceAHCI.
	for _, disk := range q.disks {
		if disk.Interface == InterfaceAHCI {
			args = append(args, "-device", "ahci,id=ahci0")
			break
		}
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

	for _, vfs := range q.virtiofsd {
		args = append(args, vfs.Args()...)
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
		// Use -display none only. -nographic would re-route the serial port
		// to stdio, fighting our explicit -serial chardev:serial0 below.
		args = append(args, "-display", "none")
	} else if q.display != "" {
		args = append(args, "-display", q.display)
	}

	if q.rtc != "" {
		args = append(args, "-rtc", q.rtc)
	}

	if q.bootOrder != "" {
		args = append(args, "-boot", fmt.Sprintf("order=%s,menu=off", q.bootOrder))
	}

	if q.qmpSocket != "" {
		args = append(args, "-qmp", fmt.Sprintf("unix:%s,server,nowait", q.qmpSocket))
	}

	// Capture the guest serial console (firmware POST, bootloader, kernel,
	// systemd, cloud-init) to a file so the boot phase watcher can tail it
	// and so users have a recoverable log even when the VM is wedged.
	serialLog := filepath.Join(q.AbsolutePath(), "serial.log")
	args = append(args, "-chardev", fmt.Sprintf("file,id=serial0,path=%s,signal=off", serialLog))
	args = append(args, "-serial", "chardev:serial0")

	if q.qgaSocket != "" {
		args = append(args, "-device", "virtio-serial-pci,id=virtio-serial0")
		args = append(args, "-chardev", fmt.Sprintf("socket,path=%s,server=on,wait=off,id=qga0", q.qgaSocket))
		args = append(args, "-device", "virtserialport,chardev=qga0,name=org.qemu.guest_agent.0")
	}

	return args
}

// SerialLogPath returns the absolute path to this machine's captured serial
// console log. The file is created by QEMU on start.
func (q *BaseMachine) SerialLogPath() string {
	return filepath.Join(q.AbsolutePath(), "serial.log")
}

// StartResult holds the outcome of a detached VM start.
type StartResult struct {
	PID       int
	QMPSocket string
	QGASocket string
}

// Start runs QEMU in the foreground (blocks until the VM exits).
func (q *BaseMachine) Start(ctx context.Context) (*StartResult, error) {
	return q.start(ctx, false)
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

	//nolint:gosec // binary/args are the operator-configured QEMU command for this VM manager, not user shell input.
	cmd := exec.CommandContext(ctx, binary, args...)

	if detach {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		// Redirect output to a log file so it is not lost.
		logPath := filepath.Join(q.AbsolutePath(), "qemu.log")
		if err := os.MkdirAll(q.AbsolutePath(), 0o750); err != nil {
			return nil, err
		}
		//nolint:gosec // logPath is derived from the VM's own storage dir (AbsolutePath()/qemu.log), not user input.
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		bootBanner := fmt.Sprintf("\n=== boot on %s ===\n", time.Now().Format(time.RFC3339))
		if _, err := fmt.Fprint(logFile, bootBanner); err != nil {
			_ = logFile.Close()
			return nil, err
		}
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		if err := cmd.Start(); err != nil {
			_ = logFile.Close()
			return nil, err
		}
		if err := q.applyVFIOLimits(cmd.Process.Pid); err != nil {
			_ = cmd.Process.Kill()
			_ = logFile.Close()
			return nil, err
		}
		go q.applyCPUPinning(cmd.Process.Pid)
		// logFile fd is inherited by the detached child; close the parent's copy.
		// The child keeps it open until it exits.
		go func() {
			_ = cmd.Wait()
			_ = logFile.Close()
		}()

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
			if _, err := os.Stat(q.qmpSocket); err != nil {
				q.provider.Logger().Warn("QMP socket did not appear — QEMU may have crashed; check qemu.log",
					zap.String("machine", q.name),
					zap.String("qmp_socket", q.qmpSocket),
					zap.Bool("process_alive", checkAlive(pid)),
				)
			} else {
				q.provider.Logger().Info("QEMU started",
					zap.String("machine", q.name),
					zap.Int("pid", pid),
					zap.String("qmp_socket", q.qmpSocket),
					zap.Bool("process_alive", checkAlive(pid)),
				)
			}
		} else {
			// No QMP socket — brief pause then check liveness.
			time.Sleep(500 * time.Millisecond)
			alive := checkAlive(pid)
			if !alive {
				q.provider.Logger().Warn("QEMU process exited immediately after start — check qemu.log",
					zap.String("machine", q.name),
					zap.Int("pid", pid),
				)
			} else {
				q.provider.Logger().Info("QEMU started",
					zap.String("machine", q.name),
					zap.Int("pid", pid),
				)
			}
		}

		return &StartResult{PID: pid, QMPSocket: q.qmpSocket, QGASocket: q.qgaSocket}, nil
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

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if err := q.applyVFIOLimits(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	go q.applyCPUPinning(cmd.Process.Pid)
	pid := cmd.Process.Pid
	// Block until QEMU exits.
	if err := cmd.Wait(); err != nil {
		return &StartResult{PID: pid}, err
	}
	return &StartResult{PID: pid}, nil
}

// applyVFIOLimits and applyCPUPinning are host-platform specific. VFIO memlock
// raising (RLIMIT_MEMLOCK) and vCPU pinning (taskset + /proc) are Linux-only;
// see machine_linux.go for the real implementations and machine_darwin.go for
// the no-op fallbacks used on macOS.

// checkAlive returns true if the process with the given PID is still running.
func checkAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks existence without disturbing the process.
	return p.Signal(syscall.Signal(0)) == nil
}

func (q *BaseMachine) AbsolutePath() string {
	return filepath.Join(q.basePath, q.name)
}

func (q *BaseMachine) Name() string {
	return q.name
}
