package vm

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Benehiko/vee/cloudinit"
	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/qemu"
	"github.com/Benehiko/vee/virtiofs"
	"go.uber.org/zap"
)

type Manager struct {
	provider provider.Provider
}

func NewManager(p provider.Provider) *Manager {
	return &Manager{provider: p}
}

func (m *Manager) storagePath() string {
	return m.provider.Config().StoragePath
}

func (m *Manager) vmDir(name string) string {
	return filepath.Join(m.storagePath(), name)
}

// Create validates and persists a new VMConfig, creating disk images and OVMF vars.
func (m *Manager) Create(ctx context.Context, cfg *VMConfig) error {
	dir := m.vmDir(cfg.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Copy OVMF_VARS.fd per-VM if UEFI is requested.
	if cfg.UEFI.Enabled {
		src := cfg.UEFI.VarsPath
		if src == "" {
			src = m.provider.Config().OVMFVarsPath
		}
		dst := filepath.Join(dir, "OVMF_VARS.fd")
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy OVMF_VARS: %w", err)
		}
		cfg.UEFI.VarsPath = dst
	}

	// Generate cloud-init cidata ISO if requested.
	if cfg.CloudInit != nil {
		writeFiles := make([]cloudinit.WriteFile, len(cfg.CloudInit.WriteFiles))
		for i, wf := range cfg.CloudInit.WriteFiles {
			writeFiles[i] = cloudinit.WriteFile{
				Path:        wf.Path,
				Content:     wf.Content,
				Permissions: wf.Permissions,
				Defer:       wf.Defer,
			}
		}
		ci := &cloudinit.Config{
			Hostname:    cfg.CloudInit.Hostname,
			User:        cfg.CloudInit.User,
			DefaultUser: cfg.CloudInit.DefaultUser,
			SSHKeys:     cfg.CloudInit.SSHKeys,
			Packages:    cfg.CloudInit.Packages,
			RunCmds:     cfg.CloudInit.RunCmds,
			WriteFiles:  writeFiles,
		}
		isoPath, err := cloudinit.Generate(dir, ci)
		if err != nil {
			return fmt.Errorf("cloud-init: %w", err)
		}
		// Append cidata ISO as an extra disk entry stored in the config.
		cfg.Disks = append(cfg.Disks, DiskConfig{
			Path:      isoPath,
			Interface: "virtio",
			Media:     "cdrom",
			Cache:     "none",
			Readonly:  true,
		})
	}

	return SaveConfig(m.storagePath(), cfg)
}

// Start launches a VM. If foreground is true it blocks; otherwise it detaches.
func (m *Manager) Start(ctx context.Context, name string, foreground bool) error {
	cfg, err := LoadConfig(m.storagePath(), name)
	if err != nil {
		return err
	}

	state, err := LoadState(m.storagePath(), name)
	if err != nil {
		return err
	}
	if state.Running {
		if isAlive(state.PID) {
			return fmt.Errorf("VM %q is already running (PID %d)", name, state.PID)
		}
		// Stale state — clean it up.
		_ = ClearState(m.storagePath(), name)
		state = &VMState{}
	}

	// Determine if an installer ISO cdrom is present.
	hasISO := false
	for _, d := range cfg.Disks {
		if d.Media == "cdrom" && strings.HasSuffix(d.Path, ".iso") {
			hasISO = true
			break
		}
	}

	switch state.InstallState {
	case "":
		// First boot: mark install as pending if there's an ISO.
		if hasISO {
			state.InstallState = InstallStatePending
			if err := SaveStateForVM(m.storagePath(), name, state); err != nil {
				return fmt.Errorf("save install state: %w", err)
			}
		}
	case InstallStateReady:
		// OS already installed — strip installer ISO cdroms so we boot from disk.
		filtered := cfg.Disks[:0]
		for _, d := range cfg.Disks {
			if d.Media == "cdrom" && strings.HasSuffix(d.Path, ".iso") {
				continue
			}
			filtered = append(filtered, d)
		}
		cfg.Disks = filtered
	}

	machine, virtiofsdPID, err := m.buildMachine(ctx, cfg)
	if err != nil {
		return err
	}

	if foreground {
		return machine.Start(ctx)
	}

	result, err := machine.StartDetached(ctx)
	if err != nil {
		return err
	}

	newState := &VMState{
		PID:          result.PID,
		QMPSocket:    result.QMPSocket,
		QGASocket:    result.QGASocket,
		VirtiofsdPID: virtiofsdPID,
		StartedAt:    ptr(time.Now()),
		Running:      true,
		InstallState: state.InstallState,
		InstalledAt:  state.InstalledAt,
	}
	if cfg.SPICE != nil {
		newState.SPICEPort = cfg.SPICE.Port
	}
	if cfg.Headless && cfg.SSHPort > 0 {
		newState.SSHPort = cfg.SSHPort
	}
	return SaveStateForVM(m.storagePath(), name, newState)
}

// WaitReady polls until SSH is accepting connections or QMP guest-agent responds,
// then marks the VM as ready (InstallState = "ready").
// timeout is how long to wait total. Polls every 5s.
func (m *Manager) WaitReady(ctx context.Context, name string, timeout time.Duration) error {
	state, err := LoadState(m.storagePath(), name)
	if err != nil {
		return err
	}
	if !state.Running || state.PID == 0 {
		return fmt.Errorf("VM %q is not running", name)
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	probe := func() bool {
		// SSH probe (headless VMs with port forwarding).
		if state.SSHPort > 0 {
			addr := fmt.Sprintf("127.0.0.1:%d", state.SSHPort)
			conn, dialErr := net.DialTimeout("tcp", addr, 2*time.Second)
			if dialErr == nil {
				_ = conn.Close()
				return true
			}
		}

		// QMP guest-agent probe (works for non-headless too).
		if state.QMPSocket != "" {
			client, qmpErr := qemu.NewQMPClient(state.QMPSocket, 2*time.Second)
			if qmpErr == nil {
				pingErr := client.GuestPing()
				_ = client.Close()
				if pingErr == nil {
					return true
				}
			}
		}

		return false
	}

	// Check immediately before first tick.
	if probe() {
		return m.markReady(name)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-ticker.C:
			if t.After(deadline) {
				return fmt.Errorf("VM %q did not become ready within %s", name, timeout)
			}
			// Reload state in case SSH port changed.
			if s, lerr := LoadState(m.storagePath(), name); lerr == nil {
				state = s
			}
			if probe() {
				return m.markReady(name)
			}
		}
	}
}

func (m *Manager) markReady(name string) error {
	state, err := LoadState(m.storagePath(), name)
	if err != nil {
		return err
	}
	state.InstallState = InstallStateReady
	state.InstalledAt = ptr(time.Now())
	return SaveStateForVM(m.storagePath(), name, state)
}

func ptr[T any](v T) *T { return &v }

// Stop sends a graceful QMP powerdown and waits for the process to exit.
func (m *Manager) Stop(ctx context.Context, name string) error {
	state, err := LoadState(m.storagePath(), name)
	if err != nil {
		return err
	}
	if !state.Running || state.PID == 0 {
		return fmt.Errorf("VM %q is not running", name)
	}

	if state.QMPSocket != "" {
		client, qmpErr := qemu.NewQMPClient(state.QMPSocket, 3*time.Second)
		if qmpErr == nil {
			_ = client.SystemPowerdown()
			_ = client.Close()
		}
	}

	// Wait up to 30s for the process to exit.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if !isAlive(state.PID) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if isAlive(state.PID) {
		proc, err := os.FindProcess(state.PID)
		if err == nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
	}

	if state.VirtiofsdPID > 0 && isAlive(state.VirtiofsdPID) {
		proc, err := os.FindProcess(state.VirtiofsdPID)
		if err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}

	// Preserve install state across stop.
	preserved := &VMState{
		InstallState: state.InstallState,
		InstalledAt:  state.InstalledAt,
	}
	return SaveStateForVM(m.storagePath(), name, preserved)
}

// Delete removes a VM directory. Refuses if the VM is running.
func (m *Manager) Delete(name string) error {
	state, err := LoadState(m.storagePath(), name)
	if err == nil && state.Running && isAlive(state.PID) {
		return fmt.Errorf("VM %q is running; stop it first", name)
	}
	return os.RemoveAll(m.vmDir(name))
}

type ListEntry struct {
	Config *VMConfig
	State  *VMState
}

// List returns all VMs with their current state.
func (m *Manager) List() ([]*ListEntry, error) {
	configs, err := ListAll(m.storagePath())
	if err != nil {
		return nil, err
	}
	var entries []*ListEntry
	for _, cfg := range configs {
		state, _ := LoadState(m.storagePath(), cfg.Name)
		if state == nil {
			state = &VMState{}
		}
		// Verify the PID is still alive.
		if state.Running && !isAlive(state.PID) {
			state.Running = false
		}
		entries = append(entries, &ListEntry{Config: cfg, State: state})
	}
	return entries, nil
}

// buildMachine constructs a BaseMachine from a VMConfig, starting virtiofsd if needed.
func (m *Manager) buildMachine(ctx context.Context, cfg *VMConfig) (*qemu.BaseMachine, int, error) {
	machine, err := qemu.NewEmptyMachine(m.provider)
	if err != nil {
		return nil, 0, err
	}

	opts := []qemu.QemuOptions{
		qemu.WithName(cfg.Name),
		qemu.WithMemory(cfg.Memory),
	}

	// CPU — gaming passthrough merges GamingCPUFlags before building.
	cpuFlags := cfg.CPUFlags
	if cfg.GPU.Mode == GPUPassthrough && cfg.GPU.AntiDetect {
		cpuFlags = append(cpuFlags, qemu.GamingCPUFlags...)
	}
	cpu := qemu.NewCPU(m.provider,
		qemu.WithCPUModel(qemu.CPUModel(cfg.CPUModel)),
		qemu.WithSMP(cfg.CPUs, cfg.Sockets, cfg.Threads, cfg.Cores),
		qemu.WithCPUFlags(cpuFlags),
	)
	opts = append(opts, qemu.WithCPU(cpu))

	// UEFI
	if cfg.UEFI.Enabled {
		codePath := cfg.UEFI.CodePath
		if codePath == "" {
			codePath = m.provider.Config().OVMFCodePath
		}
		opts = append(opts, qemu.WithUEFI(qemu.NewUEFI(codePath, cfg.UEFI.VarsPath)))
	}

	// NIC
	if cfg.NIC.MAC == "" {
		cfg.NIC.MAC = qemu.DeterministicMAC(cfg.Name)
	}
	nicHostFwds := cfg.NIC.HostFwds
	if cfg.Headless && cfg.SSHPort > 0 {
		port := availablePort(cfg.SSHPort, 2200, 2299)
		cfg.SSHPort = port
		nicHostFwds = append(nicHostFwds, fmt.Sprintf("tcp:127.0.0.1:%d-:22", port))
	}
	nic := qemu.NewNIC(qemu.NICMode(cfg.NIC.Mode), cfg.NIC.Bridge, cfg.NIC.MAC, nicHostFwds...)
	opts = append(opts, qemu.WithNIC(nic))

	// Disks
	for i, d := range cfg.Disks {
		disk := qemu.NewDisk(m.provider, machine,
			qemu.WithCustomPath(d.Path),
			qemu.WithSize(d.Size),
			qemu.WithFormat(qemu.DiskFormat(d.Format)),
			qemu.WithInterface(qemu.DiskInterface(d.Interface)),
			qemu.WithMedia(qemu.DiskMedia(d.Media)),
			qemu.WithCache(qemu.DiskCache(d.Cache)),
			qemu.WithReadonly(d.Readonly),
			qemu.WithBackingFile(d.BackingFile),
			qemu.WithSerial(d.Serial),
			qemu.WithPassthrough(d.Passthrough),
		)
		_ = i
		opts = append(opts, qemu.AddDisk(disk))
	}

	// GPU
	switch cfg.GPU.Mode {
	case GPUPassthrough:
		if cfg.GPU.PCIAddr != "" {
			opts = append(opts, qemu.WithVFIO(qemu.NewVFIODevice(cfg.GPU.PCIAddr)))
		}
		// Passthrough VMs need shared memory for the vhost-user protocol.
		opts = append(opts, qemu.WithMemfd(qemu.NewMemfdBackend(cfg.Memory)))
	case GPUVirtio:
		opts = append(opts, qemu.WithVGA("none"))
		opts = append(opts, qemu.WithDevice("virtio-vga-gl"))
		opts = append(opts, qemu.WithDisplay("gtk,gl=on"))
	}

	// Headless
	if cfg.Headless {
		opts = append(opts, qemu.WithHeadless())
	}

	// SPICE
	if cfg.SPICE != nil {
		spice := qemu.NewSpice(
			qemu.WithSpicePort(cfg.SPICE.Port),
			qemu.WithSpiceDisableTicketing(cfg.SPICE.DisableTicketing),
		)
		opts = append(opts, qemu.WithSpice(spice))
	}

	// QMP socket
	qmpSock := filepath.Join(m.vmDir(cfg.Name), "qmp.sock")
	opts = append(opts, qemu.WithQMPSocket(qmpSock))

	// QGA socket
	if cfg.GuestAgent {
		qgaSock := filepath.Join(m.vmDir(cfg.Name), "qga.sock")
		opts = append(opts, qemu.WithQGASocket(qgaSock))
	}

	// Extra devices (e.g. virtio-serial-pci for guest agent)
	for _, dev := range cfg.ExtraDevices {
		opts = append(opts, qemu.WithDevice(dev))
	}

	// Virtiofsd mounts
	virtiofsdPID := 0
	for _, mount := range cfg.VirtiofsMounts {
		sockPath := mount.SocketPath
		if sockPath == "" {
			sockPath = filepath.Join(m.vmDir(cfg.Name), mount.Tag+".sock")
		}

		vd := virtiofs.NewVirtiofsd(m.provider,
			virtiofs.WithVirtiofsdSocketPath(sockPath),
			virtiofs.WithVirtiofsdSharedDir(mount.SharedDir),
		)
		pid, err := vd.StartDetached(ctx)
		if err != nil {
			m.provider.Logger().Warn("failed to start virtiofsd",
				zap.String("tag", mount.Tag),
				zap.Error(err))
		} else {
			virtiofsdPID = pid
		}
		opts = append(opts, qemu.WithVirtiofsd(sockPath, mount.Tag))
	}

	// TPM — start swtpm daemon before QEMU.
	if cfg.TPM != nil && cfg.TPM.Enabled {
		tpmDir := filepath.Join(m.vmDir(cfg.Name), "tpm")
		tpmSock := filepath.Join(m.vmDir(cfg.Name), "tpm.sock")
		if err := startSwtpm(tpmDir, tpmSock); err != nil {
			return nil, 0, fmt.Errorf("swtpm: %w", err)
		}
		opts = append(opts, qemu.WithTPM(qemu.NewTPM(tpmSock)))
	}

	// VSock — add vhost-vsock-pci device when SSH sharing is enabled.
	if cfg.SSHShare {
		cid := cfg.VsockCID
		if cid == 0 {
			cid = deterministicCID(cfg.Name)
		}
		opts = append(opts, qemu.WithVSock(qemu.NewVSockDevice(cid)))
	}

	built, err := machine.BuildMachine(opts...)
	if err != nil {
		return nil, 0, err
	}

	return built, virtiofsdPID, nil
}

// startSwtpm launches a swtpm daemon for the given VM TPM state directory.
// swtpm must be installed on the host.
func startSwtpm(stateDir, socketPath string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	// swtpm socket --tpmstate dir=<dir> --ctrl type=unixio,path=<sock> --tpm2 --daemon
	cmd := newSwtpmCmd(stateDir, socketPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	// Give swtpm a moment to create the socket.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("swtpm socket not created at %s", socketPath)
}

func newSwtpmCmd(stateDir, socketPath string) *exec.Cmd {
	return exec.Command("swtpm", "socket",
		"--tpmstate", "dir="+stateDir,
		"--ctrl", "type=unixio,path="+socketPath,
		"--tpm2",
		"--daemon",
	)
}

// deterministicCID returns a stable guest CID (>= 3) derived from the VM name.
// CIDs 0-2 are reserved: hypervisor, local, host.
func deterministicCID(name string) uint32 {
	h := sha1.Sum([]byte(name))
	cid := uint32(h[0])<<24 | uint32(h[1])<<16 | uint32(h[2])<<8 | uint32(h[3])
	if cid < 3 {
		cid += 3
	}
	return cid
}

// availablePort returns preferred if it is free, otherwise scans [min, max] for
// the first free port. Falls back to preferred if none found (QEMU will error).
func availablePort(preferred, min, max int) int {
	if isPortFree(preferred) {
		return preferred
	}
	for p := min; p <= max; p++ {
		if p != preferred && isPortFree(p) {
			return p
		}
	}
	return preferred
}

func isPortFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	return err
}
