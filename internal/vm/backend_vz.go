package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/platform"
	"github.com/Benehiko/vee/internal/qemu"
	"github.com/Benehiko/vee/internal/vzfirstboot"
	"github.com/Benehiko/vee/internal/vzhelper"
)

// vzHelperBinary aliases the shared helper name (resolution lives in
// vzhelper.ResolveHelper so images/templates can reuse it).
const vzHelperBinary = vzhelper.HelperBinary

// vzStartTimeout bounds how long StartDetached waits for the helper's
// control socket to appear (the QMP-socket-appears analog).
const vzStartTimeout = 10 * time.Second

// buildVZMachine validates a vz-backend config, writes the machine spec into
// the VM directory and returns a Machine that spawns the helper. A config
// with a macos: section runs a macOS guest (restored bundle: raw disk image,
// auxiliary storage, hardware-model and machine-identifier blobs — issue
// #51); without one it runs a Linux guest booted via EFI from a whole-disk
// raw image (issue #127).
func (m *Manager) buildVZMachine(_ context.Context, cfg *VMConfig) (backend.Machine, error) {
	if !platform.IsMacOS() || platform.HostArch() != "arm64" {
		return nil, fmt.Errorf("the vz backend requires an Apple Silicon macOS host (got %s/%s)",
			platform.HostOS(), platform.HostArch())
	}
	if cfg.SSHPort > 0 {
		return nil, fmt.Errorf("the vz backend does not support ssh_port: NAT has no host port-forwarding — remove ssh_port and use `vee ssh` (resolves the guest IP by MAC)")
	}
	// Create() refuses this pairing, but a hand-edited vm.yaml reaches here
	// directly (vee start, daemon autostart) — refuse like ssh_port above
	// rather than silently starting a guest that can never nest.
	if cfg.Nested {
		return nil, fmt.Errorf("the vz backend does not support nested virtualization: Virtualization.framework does not expose EL2 to its guests — remove nested from vm.yaml")
	}

	// The NAT guest's IP is discovered by MAC, so the MAC must be stable.
	if cfg.NIC.MAC == "" {
		cfg.NIC.MAC = qemu.DeterministicMAC(cfg.Name)
	}

	memBytes, err := vzhelper.ParseMemoryBytes(cfg.Memory)
	if err != nil {
		return nil, err
	}
	if cfg.CPUs <= 0 {
		return nil, fmt.Errorf("the vz backend requires cpus > 0")
	}
	cpus := uint64(cfg.CPUs) //nolint:gosec // guarded > 0 above
	if cfg.MacOS != nil {
		// Enforce the restored image's recorded minimums at start too —
		// configs can be hand-edited below them, and a guest under the
		// minimums will not boot.
		if cfg.MacOS.MinCPUs > 0 && cpus < cfg.MacOS.MinCPUs {
			cpus = cfg.MacOS.MinCPUs
		}
		if cfg.MacOS.MinMemoryBytes > 0 && memBytes < cfg.MacOS.MinMemoryBytes {
			memBytes = cfg.MacOS.MinMemoryBytes
		}
	}

	vmDir := m.vmDir(cfg.Name)

	disks, err := m.vzDiskSpecs(cfg)
	if err != nil {
		return nil, err
	}

	// ResolveHelper also heals a quarantined helper; a failure there is fatal
	// because Gatekeeper would SIGKILL the helper on exec.
	helperPath, err := vzhelper.ResolveHelper()
	if err != nil {
		return nil, err
	}

	spec := &vzhelper.MachineSpec{
		Name:        cfg.Name,
		CPUs:        uint(cpus), //nolint:gosec // VM CPU counts are tiny
		MemoryBytes: memBytes,
		MAC:         cfg.NIC.MAC,
		Disks:       disks,
		Vsock:       cfg.Vsock,
	}

	if cfg.MacOS != nil {
		auxPath := cfg.MacOS.AuxiliaryStorage
		if auxPath != "" && !filepath.IsAbs(auxPath) {
			return nil, fmt.Errorf("the vz backend requires an absolute auxiliary_storage path (got %q)", auxPath)
		}
		display := vzhelper.DefaultDisplay
		if cfg.MacOS.DisplayWidthPx > 0 && cfg.MacOS.DisplayHeightPx > 0 {
			display = vzhelper.DisplaySpec{
				WidthPx:  cfg.MacOS.DisplayWidthPx,
				HeightPx: cfg.MacOS.DisplayHeightPx,
				PPI:      cfg.MacOS.DisplayPPI,
			}
			if display.PPI <= 0 {
				display.PPI = vzhelper.DefaultDisplay.PPI
			}
		}
		spec.Platform = vzhelper.PlatformMacOS
		spec.AuxiliaryStorage = auxPath
		spec.HardwareModel = cfg.MacOS.HardwareModel
		spec.MachineIdentifier = cfg.MacOS.MachineIdentifier
		spec.Display = display
	} else {
		// Linux guest: EFI boot, headless, console captured to the serial
		// log. The variable store is created by the helper on first boot.
		spec.Platform = vzhelper.PlatformLinux
		spec.EFIVariableStore = vzhelper.EFIVariableStorePath(vmDir)
		spec.SerialLog = vzhelper.SerialLogPath(vmDir)
	}

	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if err := vzhelper.WriteSpec(vmDir, spec); err != nil {
		return nil, fmt.Errorf("write vz machine spec: %w", err)
	}

	return &vzMachine{
		manager:    m,
		name:       cfg.Name,
		vmDir:      vmDir,
		helperPath: helperPath,
		mac:        cfg.NIC.MAC,
		// disks[0] is the boot disk; Validate above rejects an empty list.
		diskPath: disks[0].Path,
		cfg:      cfg,
	}, nil
}

// prepareVZLinuxCreate normalizes a template-produced config for a Linux
// guest on the vz backend (issue #127) and materializes its disks. Templates
// are written against QEMU's device model, so the differences are resolved
// here — at create, where the user sees them — rather than failing at first
// start: QEMU-only devices are refused (or dropped when losing them is
// harmless), and qcow2 boot disks become raw images, the only format
// Virtualization.framework reads.
func (m *Manager) prepareVZLinuxCreate(ctx context.Context, cfg *VMConfig) error {
	if !platform.IsMacOS() || platform.HostArch() != "arm64" {
		return fmt.Errorf("the vz backend requires an Apple Silicon macOS host (got %s/%s)",
			platform.HostOS(), platform.HostArch())
	}
	if cfg.GPU.Mode != "" && cfg.GPU.Mode != GPUNone {
		return fmt.Errorf("the vz backend has no GPU support: Linux guests run headless (gpu mode %q) — use the QEMU backend for graphical guests", cfg.GPU.Mode)
	}
	if cfg.TPM != nil && cfg.TPM.Enabled {
		return fmt.Errorf("the vz backend does not support a TPM device — use the QEMU backend")
	}
	if len(cfg.VirtiofsMounts) > 0 {
		return fmt.Errorf("the vz backend does not support virtiofs shares for Linux guests yet — use the QEMU backend, or drop the share")
	}
	if cfg.NIC.Mode == "bridge" {
		return fmt.Errorf("the vz backend attaches guests to NAT only — bridge networking is not supported; use --nic-mode=user or the QEMU backend")
	}
	for _, d := range cfg.Disks {
		if d.Passthrough {
			return fmt.Errorf("the vz backend does not support raw device passthrough (disk %s) — use the QEMU backend", d.Path)
		}
	}

	log := m.provider.Logger()
	// Dropped rather than refused: the guest works without them, they are
	// template defaults the user did not necessarily ask for, and each has a
	// vz-native replacement.
	if cfg.SPICE != nil {
		log.Info("vz: dropping the SPICE display — Linux guests on Virtualization.framework are headless (use `vee ssh`)",
			zap.String("vm", cfg.Name))
		cfg.SPICE = nil
		services := cfg.Services[:0]
		for _, s := range cfg.Services {
			if s.Protocol != ServiceSPICE {
				services = append(services, s)
			}
		}
		cfg.Services = services
	}
	if cfg.SSHPort > 0 {
		log.Info("vz: dropping ssh_port — vz NAT has no host port-forwarding; `vee ssh` resolves the guest IP by MAC",
			zap.String("vm", cfg.Name), zap.Int("ssh_port", cfg.SSHPort))
		cfg.SSHPort = 0
	}
	// The vz backend carries its own EFI variable store; OVMF pflash is a
	// QEMU concept and must not be copied into the VM directory.
	cfg.UEFI = UEFIConfig{}

	return m.materializeVZLinuxDisks(ctx, cfg)
}

// materializeVZLinuxDisks turns every writable disk of a vz Linux guest into
// a raw image on disk: qcow2 overlays over a cloud image are flattened with
// qemu-img convert (then grown to the configured size), and blank disks are
// created raw. cdrom entries (the cidata seed) are left alone. Idempotent —
// an already-materialized disk is kept, mirroring qemu.Disk.Create.
func (m *Manager) materializeVZLinuxDisks(ctx context.Context, cfg *VMConfig) error {
	for i := range cfg.Disks {
		d := &cfg.Disks[i]
		if d.Media == "cdrom" {
			continue
		}
		switch d.Format {
		case "", "raw", "qcow2":
			if err := m.materializeVZDisk(ctx, cfg.Name, d); err != nil {
				return err
			}
		default:
			return fmt.Errorf("the vz backend cannot use disk %s (format %q)", d.Path, d.Format)
		}
	}
	return nil
}

// materializeVZDisk creates one raw disk image for d and repoints the config
// entry at it: converted from its qcow2 backing file when it has one, blank
// otherwise.
func (m *Manager) materializeVZDisk(ctx context.Context, vmName string, d *DiskConfig) error {
	target := vzRawDiskPath(d.Path)
	log := m.provider.Logger()

	if _, err := os.Stat(target); err == nil {
		log.Info("vz: skipping disk creation", zap.String("reason", "disk already exists"),
			zap.String("vm", vmName), zap.String("path", target))
		vzRepointDisk(d, target)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}

	qemuImg, err := vzQemuImgPath()
	if err != nil {
		return err
	}

	if d.BackingFile != "" {
		// Flatten the cloud image into a standalone raw disk. qemu-img writes
		// the output sparse, so the file only occupies what the image holds.
		//nolint:gosec // qemu-img resolved from vee-managed locations; args are vee-derived disk paths
		if out, err := exec.CommandContext(ctx, qemuImg, "convert", "-O", "raw", d.BackingFile, target).CombinedOutput(); err != nil {
			_ = os.Remove(target)
			return fmt.Errorf("convert %s to raw: %w: %s", d.BackingFile, err, strings.TrimSpace(string(out)))
		}
		if d.Size != "" {
			//nolint:gosec // qemu-img resolved from vee-managed locations; args are vee-derived disk paths
			if out, err := exec.CommandContext(ctx, qemuImg, "resize", "-f", "raw", target, d.Size).CombinedOutput(); err != nil {
				_ = os.Remove(target)
				return fmt.Errorf("resize %s to %s: %w: %s", target, d.Size, err, strings.TrimSpace(string(out)))
			}
		}
		log.Info("vz: converted cloud image to raw boot disk",
			zap.String("vm", vmName), zap.String("backing", d.BackingFile), zap.String("path", target))
	} else {
		//nolint:gosec // qemu-img resolved from vee-managed locations; args are vee-derived disk paths
		if out, err := exec.CommandContext(ctx, qemuImg, "create", "-f", "raw", target, d.Size).CombinedOutput(); err != nil {
			_ = os.Remove(target)
			return fmt.Errorf("create raw disk %s: %w: %s", target, err, strings.TrimSpace(string(out)))
		}
		log.Info("vz: created raw disk", zap.String("vm", vmName), zap.String("path", target))
	}
	vzRepointDisk(d, target)
	return nil
}

// vzRepointDisk rewrites a config disk entry to its materialized raw image.
// The backing file reference is cleared — the raw image is standalone — and
// the QEMU-only cache hint is dropped.
func vzRepointDisk(d *DiskConfig, target string) {
	d.Path = target
	d.Format = "raw"
	d.BackingFile = ""
	d.Cache = ""
}

// vzRawDiskPath derives the raw image path for a configured disk: a known
// image suffix is replaced with .raw, and a bare directory (qemu.Disk's
// "generate the name" form) gets a fixed file name joined on.
func vzRawDiskPath(path string) string {
	for _, suffix := range []string{".qcow2", ".qcow", ".img", ".vmdk", ".vdi", ".vhd"} {
		if strings.HasSuffix(path, suffix) {
			return strings.TrimSuffix(path, suffix) + ".raw"
		}
	}
	if strings.HasSuffix(path, ".raw") {
		return path
	}
	return filepath.Join(path, "disk-os.raw")
}

// vzQemuImgPath locates qemu-img for the one-shot raw conversion at create
// time: the vee-managed bin dir (the bundled QEMU ships it), then PATH.
func vzQemuImgPath() (string, error) {
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".vee", "bin", "qemu-img")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath("qemu-img"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("qemu-img not found (looked in ~/.vee/bin and $PATH) — it is needed once, to prepare the guest's raw boot disk; install it with `brew install qemu`")
}

// vzDiskSpecs translates the config's disks into helper disk attachments.
// Paths must be absolute: vee's other disk consumers (data-presence checks,
// boot-disk moves) resolve relative paths against the process CWD, so
// accepting them here would give one field two meanings. cdrom-media disks
// (the cloud-init cidata seed) attach read-only, and one whose backing file
// is gone is skipped like the QEMU backend skips it — the install-state
// machine strips such disks once provisioning completes, but a stale config
// can still reach a start.
func (m *Manager) vzDiskSpecs(cfg *VMConfig) ([]vzhelper.DiskSpec, error) {
	disks := make([]vzhelper.DiskSpec, 0, len(cfg.Disks))
	for _, d := range cfg.Disks {
		if d.Passthrough {
			return nil, fmt.Errorf("the vz backend does not support raw device passthrough (disk %s)", d.Path)
		}
		if d.Format != "" && d.Format != "raw" {
			return nil, fmt.Errorf("the vz backend supports raw disk images only (disk %s has format %q)", d.Path, d.Format)
		}
		if !filepath.IsAbs(d.Path) {
			return nil, fmt.Errorf("the vz backend requires absolute disk paths (got %q)", d.Path)
		}
		if d.Media == "cdrom" {
			if _, err := os.Stat(d.Path); os.IsNotExist(err) {
				m.provider.Logger().Warn("skipping cdrom disk: backing file is missing",
					zap.String("vm", cfg.Name), zap.String("path", d.Path))
				continue
			}
			disks = append(disks, vzhelper.DiskSpec{Path: d.Path, ReadOnly: true})
			continue
		}
		disks = append(disks, vzhelper.DiskSpec{Path: d.Path, ReadOnly: d.Readonly})
	}
	return disks, nil
}

// vzMachine implements backend.Machine by spawning a detached vee-vz-helper
// that owns the VZVirtualMachine for the VM's lifetime.
type vzMachine struct {
	manager    *Manager
	name       string
	vmDir      string
	helperPath string
	// mac is the guest NIC address, used to snapshot its DHCP lease before
	// the guest can boot.
	mac string
	// diskPath and cfg drive the start-time Screen Sharing grant, which
	// rewrites cfg once it succeeds so later starts skip it.
	diskPath string
	cfg      *VMConfig
}

// ensureScreenSharingGrants is a seam so the retry state machine can be tested
// without a guest disk to attach.
var ensureScreenSharingGrants = vzfirstboot.EnsureScreenSharingGrants

// grantScreenSharing authorizes the guest's screen-sharing agent while the
// disk is still idle, if provisioning could not: macOS creates the privacy
// database vee writes into on the guest's first boot, not during the restore,
// so the grant has to be retried later than the patch that asked for it.
//
// Every failure is survivable — the guest boots either way, and only Screen
// Sharing is affected — so nothing here fails a start.
func (v *vzMachine) grantScreenSharing(ctx context.Context) error {
	if v.cfg.MacOS == nil || !v.cfg.MacOS.ScreenSharingGrantPending {
		return nil
	}
	log := v.manager.provider.Logger()
	granted, err := ensureScreenSharingGrants(ctx, v.diskPath)
	switch {
	case errors.Is(err, vzfirstboot.ErrGuestDiskBusy):
		// The only failure that must stop the start: booting a disk the host
		// still has attached fails in Virtualization.framework anyway, and a
		// mount left inside the VM directory is worse than a failed start.
		return fmt.Errorf("%w\nRun `hdiutil detach` on the guest disk (%s), then start the VM again", err, v.diskPath)
	case errors.Is(err, vzfirstboot.ErrNoTCCDB):
		// The normal state until the guest has booted once.
		log.Debug("vz: guest has no privacy database yet; Screen Sharing will be authorized at a later start",
			zap.String("vm", v.name))
		return nil
	case errors.Is(err, vzfirstboot.ErrUnknownTCCSchema):
		// Never resolves on its own, so stop attaching the disk for it — and
		// record why, so nothing downstream reports this guest as authorized.
		log.Warn("vz: this guest's privacy database is not one vee recognizes; enable Screen Sharing from the guest's System Settings",
			zap.String("vm", v.name), zap.Error(err))
		v.cfg.MacOS.ScreenSharingUnsupported = true
	case err != nil:
		log.Warn("vz: could not authorize Screen Sharing in the guest",
			zap.String("vm", v.name), zap.Error(err))
		return nil
	case granted:
		log.Info("vz: authorized Screen Sharing in the guest",
			zap.String("vm", v.name))
	}

	// Stop attaching the disk at every start, now that there is nothing left to
	// write. A failed record only costs one redundant check next time.
	v.cfg.MacOS.ScreenSharingGrantPending = false
	if err := v.manager.saveConfig(v.cfg); err != nil {
		log.Warn("vz: could not record that Screen Sharing needs no further attempt",
			zap.String("vm", v.name), zap.Error(err))
	}
	return nil
}

func (v *vzMachine) StartDetached(ctx context.Context) (*backend.StartResult, error) {
	sockPath := vzhelper.ControlSocketPath(v.vmDir)
	_ = os.Remove(sockPath)

	logPath := vzhelper.LogPath(v.vmDir)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // path derived from vee-managed VM directory
	if err != nil {
		return nil, fmt.Errorf("open helper log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	v.manager.provider.Logger().Info("starting vz helper",
		zap.String("machine", v.name),
		zap.String("binary", v.helperPath),
		zap.String("vm_dir", v.vmDir))

	if err := v.grantScreenSharing(ctx); err != nil {
		return nil, err
	}

	// Snapshot the guest's DHCP-lease expiry before the VM can possibly boot:
	// readiness is "the lease expiry advanced past this", and a guest can
	// acquire its lease within a second of starting.
	leaseBaseline := dhcpLeaseExpiry(v.mac)

	// The helper owns the VM for its lifetime, so it must outlive the CLI that
	// started it: exec.CommandContext kills the child when its context is
	// cancelled, and vee's root command cancels its signal context as soon as
	// the command returns.
	//nolint:gosec // helperPath is resolved from vee-managed locations, vmDir from vee storage
	cmd := exec.CommandContext(context.WithoutCancel(ctx), v.helperPath, "--vm-dir", v.vmDir)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setDetachAttrs(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", vzHelperBinary, err)
	}
	pid := cmd.Process.Pid
	// Reap the child when it exits (mirrors the QEMU launch path). Without
	// this an exited helper stays a zombie of the calling process and
	// signal-0 liveness checks keep reporting it alive.
	helperExited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(helperExited)
	}()

	// The control socket appearing is the "VM is up" gate: the helper binds
	// it only after VZVirtualMachine.Start succeeds, so its existence means
	// the VM actually started (mirrors the QMP-socket wait on QEMU).
	deadline := time.Now().Add(vzStartTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			return &backend.StartResult{PID: pid, ControlSocket: sockPath, LeaseBaseline: leaseBaseline}, nil
		}
		select {
		case <-helperExited:
			if msg := vzStartFailureDetail(v.vmDir); msg != "" {
				return nil, fmt.Errorf("%s failed to start the VM: %s (full log: %s)", vzHelperBinary, msg, logPath)
			}
			return nil, fmt.Errorf("%s exited during startup — check %s (a common cause is a binary missing the com.apple.security.virtualization entitlement; rebuild with `make vz-helper`)", vzHelperBinary, logPath)
		case <-time.After(100 * time.Millisecond):
		}
	}
	// Timed out with the helper still alive: kill it rather than leaving an
	// untracked helper that could finish starting later and double-attach
	// the raw disk images on the next vee start.
	if proc, findErr := os.FindProcess(pid); findErr == nil {
		_ = proc.Kill()
	}
	return nil, fmt.Errorf("%s did not open its control socket within %s (helper killed) — check %s", vzHelperBinary, vzStartTimeout, logPath)
}

// vzStartFailureDetail surfaces the helper's recorded start error, if any.
func vzStartFailureDetail(vmDir string) string {
	res, err := vzhelper.LoadResult(vmDir)
	if err != nil {
		return ""
	}
	return res.Error
}

// vzControlRequest sends one request to a helper control socket and returns
// the response.
func vzControlRequest(ctx context.Context, sockPath string, req *vzhelper.Request, timeout time.Duration) (*vzhelper.Response, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "unix", sockPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	var resp vzhelper.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// vzVsockOpTimeout bounds the vsock control ops: the helper answers them from
// memory (it does not wait on the guest), so a slow response means a wedged
// helper, not a booting guest.
const vzVsockOpTimeout = 10 * time.Second

// VZVsockConnect opens a host→guest virtio-vsock connection to a port the
// guest is listening on (AF_VSOCK), for a running vz-backend VM. The helper
// bridges the framework's socket device onto a unix socket in the VM
// directory; the returned conn is one hop through that bridge.
func (m *Manager) VZVsockConnect(ctx context.Context, name string, port uint32) (net.Conn, error) {
	sockPath, err := m.vzVsockControlSocket(name)
	if err != nil {
		return nil, err
	}
	resp, err := vzControlRequest(ctx, sockPath, &vzhelper.Request{Op: vzhelper.OpVsockConnect, Port: port}, vzVsockOpTimeout)
	if err != nil {
		return nil, fmt.Errorf("vsock-connect %s port %d: %w", name, port, err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("vsock-connect %s port %d: %w", name, port, vzVsockOpError(resp.Error))
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", resp.Path)
	if err != nil {
		return nil, fmt.Errorf("dial vsock bridge %s: %w", resp.Path, err)
	}
	return conn, nil
}

// VZVsockListen forwards guest-initiated virtio-vsock connections to port
// into the host unix socket at hostSocket (which the caller must be listening
// on), for a running vz-backend VM. Repeating the call retargets the forward.
func (m *Manager) VZVsockListen(ctx context.Context, name string, port uint32, hostSocket string) error {
	sockPath, err := m.vzVsockControlSocket(name)
	if err != nil {
		return err
	}
	resp, err := vzControlRequest(ctx, sockPath, &vzhelper.Request{Op: vzhelper.OpVsockListen, Port: port, Path: hostSocket}, vzVsockOpTimeout)
	if err != nil {
		return fmt.Errorf("vsock-listen %s port %d: %w", name, port, err)
	}
	if !resp.OK {
		return fmt.Errorf("vsock-listen %s port %d: %w", name, port, vzVsockOpError(resp.Error))
	}
	return nil
}

// vzVsockControlSocket validates that a VM can serve vsock ops — vz backend,
// vsock enabled, running — and returns its helper control socket.
func (m *Manager) vzVsockControlSocket(name string) (string, error) {
	cfg, err := m.loadConfig(name)
	if err != nil {
		return "", err
	}
	if cfg.BackendName() != backend.VZ {
		return "", fmt.Errorf("VM %q does not use the vz backend — its vsock channel is not driven through the helper control protocol", name)
	}
	if !cfg.Vsock {
		return "", fmt.Errorf("VM %q has no vsock device — set vsock: true in its vm.yaml and restart it", name)
	}
	state, err := m.LoadState(name)
	if err != nil || state.ControlSocket == "" || !isAlive(state.PID) {
		return "", fmt.Errorf("VM %q is not running", name)
	}
	return state.ControlSocket, nil
}

// vzVsockOpError upgrades a helper-reported vsock failure into an actionable
// error: an "unknown op" answer means the running helper predates the vsock
// control ops (protocol version 0, issue #61).
func vzVsockOpError(msg string) error {
	if strings.Contains(msg, "unknown op") {
		return fmt.Errorf("%s — the running %s predates vsock support (vee speaks control protocol v%d); update the helper (re-extract the release tarball or `make vz-helper`) and restart the VM", msg, vzhelper.HelperBinary, vzhelper.ProtocolVersion)
	}
	return errors.New(msg)
}

// waitReadyVZ polls until the vz guest is reachable: readiness means the
// guest acquired a FRESH DHCP lease. bootpd keeps stale entries in
// /var/db/dhcpd_leases across shutdowns (even past expiry), so a bare MAC
// match proves nothing on a restart — instead the lease expiry recorded for
// the MAC must ADVANCE past the baseline taken before the VM started (every
// DHCP grant/renewal rewrites it). A fresh macOS guest has no SSH enabled
// yet, so the lease is the strongest "the guest is up" signal available.
//
// Newer macOS hosts no longer maintain /var/db/dhcpd_leases for vmnet
// guests at all (observed on macOS 26: live NAT guests appear in the ARP
// table but never in the lease file), so a lease that never advances falls
// back to dialling the guest's SSH port at its ARP-resolved address — vee's
// guests all serve SSH (cloud-init templates run sshd; macOS provisioning
// enables Remote Login), and an answer on the MAC-bound IP can only come
// from this guest.
func (m *Manager) waitReadyVZ(ctx context.Context, name string, state *VMState, timeout time.Duration) error {
	exitedErr := func() error {
		return fmt.Errorf("VM %q process (PID %d) exited — check %s", name, state.PID, vzhelper.LogPath(m.vmDir(name)))
	}
	if !isAlive(state.PID) {
		return exitedErr()
	}

	var mac string
	if cfg, err := m.loadConfig(name); err == nil {
		mac = cfg.NIC.MAC
	}
	if mac == "" {
		// Nothing to probe; process liveness is the best available signal.
		return m.markReady(name)
	}

	// The baseline was taken before the VM started (see StartDetached); a lease
	// the guest acquired during startup therefore still counts as ready.
	baseline := state.LeaseBaseline
	probe := func() bool {
		exp := dhcpLeaseExpiry(mac)
		if exp > 0 && exp > baseline {
			return true
		}
		return vzGuestSSHReachable(ctx, mac)
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-ticker.C:
			if !isAlive(state.PID) {
				return exitedErr()
			}
			if probe() {
				return m.markReady(name)
			}
			if t.After(deadline) {
				return fmt.Errorf("VM %q did not acquire a DHCP lease within %s (MAC %s) — the guest may still be booting; check its screen or %s", name, timeout, mac, vzhelper.LogPath(m.vmDir(name)))
			}
		}
	}
}

// vzGuestSSHReachable reports whether something answers on the SSH port of
// the guest's MAC-resolved IP — the readiness fallback for hosts whose bootpd
// no longer records vmnet leases. The dial is short: the guest is on a local
// NAT bridge, so anything longer than a moment means "not up yet".
func vzGuestSSHReachable(ctx context.Context, mac string) bool {
	ip, err := ResolveIPFromMAC(mac)
	if err != nil {
		return false
	}
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, "22"))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// watchVZShutdown blocks on the helper's wait-shutdown op and records a
// guest-initiated shutdown the same way the QMP SHUTDOWN watcher does for
// QEMU VMs. Best-effort; runs in a goroutine that outlives Start.
func (m *Manager) watchVZShutdown(ctx context.Context, name, sockPath string) {
	log := m.provider.Logger()
	// No timeout: the op blocks for the VM's lifetime, like the QMP owner
	// connection.
	resp, err := vzControlRequest(ctx, sockPath, &vzhelper.Request{Op: vzhelper.OpWaitShutdown}, 0)
	switch {
	case err != nil:
		// The helper may have exited before its response landed (or the
		// daemon attached mid-shutdown). The durable result file is the
		// fallback record.
		log.Debug("vz shutdown watcher: wait failed, checking result file",
			zap.String("vm", name), zap.Error(err))
		time.Sleep(500 * time.Millisecond)
		res, loadErr := vzhelper.LoadResult(m.vmDir(name))
		if loadErr != nil || res.Error != "" || res.StopRequested {
			// No record / crash / host stop — leave the reason to Stop or
			// stale-VM cleanup.
			return
		}
	case resp.Reason != vzhelper.ReasonGuest:
		// Host-initiated (Stop recorded the reason already) or an internal
		// VM error (the crash analog — stale cleanup records it so the
		// daemon's autostart recovery still fires).
		return
	}
	m.recordGuestShutdown(name)
}

// vzShutdownTimeout bounds the ssh call that asks the guest to power off. It
// is short because the caller has its own wait-then-kill budget, and because
// a guest that cannot be reached will not answer at all.
const vzShutdownTimeout = 10 * time.Second

// vzShutdownOverSSH asks a vz guest to power itself off. macOS ignores
// VZVirtualMachine.requestStop — the ACPI-powerdown analog — so without this
// every `vee stop` waits out its grace period and then SIGKILLs the VM, which
// leaves the guest filesystem unclean. No password is needed: macOS
// provisioning installs a sudoers rule granting exactly /sbin/shutdown, and
// Linux cloud-init users carry passwordless sudo.
func (m *Manager) vzShutdownOverSSH(ctx context.Context, name string) error {
	cfg, err := m.loadConfig(name)
	if err != nil {
		return err
	}
	if cfg.SSHUsername() == "" {
		return fmt.Errorf("no ssh user recorded for %q", name)
	}
	if cfg.NIC.MAC == "" {
		return fmt.Errorf("no MAC recorded for %q", name)
	}
	ip, err := ResolveIPFromMAC(cfg.NIC.MAC)
	if err != nil {
		return fmt.Errorf("resolve guest IP: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, vzShutdownTimeout)
	defer cancel()
	//nolint:gosec // ssh from LookPath; arguments are vee-derived
	out, err := exec.CommandContext(shutdownCtx, sshBin, vzShutdownArgs(cfg.SSHUsername(), ip, home)...).CombinedOutput()
	if err != nil {
		// Losing the connection is the expected outcome of a successful
		// shutdown, so only a refusal is worth reporting.
		if strings.Contains(string(out), "closed by remote host") || strings.Contains(string(out), "Connection to") {
			return nil
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// vzShutdownArgs builds the ssh invocation used to power a guest off. It never
// prompts: BatchMode refuses password authentication, `sudo -n` refuses to ask
// for a password, and host-key changes are accepted rather than blocking — vee
// tracks guest identity itself, and a recreated guest legitimately has a new
// key.
func vzShutdownArgs(user, ip, home string) []string {
	return []string{
		"-i", filepath.Join(home, ".vee", "ssh", "id_ed25519"),
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=" + filepath.Join(home, ".vee", "ssh", "known_hosts"),
		"-o", "ConnectTimeout=5",
		user + "@" + ip,
		"sudo -n /sbin/shutdown -h now",
	}
}
