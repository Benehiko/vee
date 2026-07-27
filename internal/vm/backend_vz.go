package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/platform"
	"github.com/Benehiko/vee/internal/qemu"
	"github.com/Benehiko/vee/internal/vzhelper"
)

// vzHelperBinary is the name of the Virtualization.framework helper that
// hosts one macOS guest per process (cmd/vee-vz-helper).
const vzHelperBinary = "vee-vz-helper"

// vzStartTimeout bounds how long StartDetached waits for the helper's
// control socket to appear (the QMP-socket-appears analog).
const vzStartTimeout = 10 * time.Second

// buildVZMachine validates a vz-backend config, writes the machine spec into
// the VM directory and returns a Machine that spawns the helper. Manual
// prerequisite until the restore flow lands (#51 V5): a restored macOS
// bundle (raw disk image, auxiliary storage, hardware-model and
// machine-identifier blobs) referenced from the config's macos: section.
func (m *Manager) buildVZMachine(_ context.Context, cfg *VMConfig) (backend.Machine, error) {
	if !platform.IsMacOS() || platform.HostArch() != "arm64" {
		return nil, fmt.Errorf("the vz backend requires an Apple Silicon macOS host (got %s/%s)",
			platform.HostOS(), platform.HostArch())
	}
	if cfg.MacOS == nil {
		return nil, fmt.Errorf("the vz backend requires a macos: section in vm.yaml (auxiliary_storage, hardware_model, machine_identifier — produced by a macOS restore, importable from a macosvm bundle); see https://github.com/Benehiko/vee/issues/51")
	}

	helperPath, err := resolveVZHelper()
	if err != nil {
		return nil, err
	}

	vmDir := m.vmDir(cfg.Name)

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

	disks := make([]vzhelper.DiskSpec, 0, len(cfg.Disks))
	for _, d := range cfg.Disks {
		if d.Format != "" && d.Format != "raw" {
			return nil, fmt.Errorf("the vz backend supports raw disk images only (disk %s has format %q)", d.Path, d.Format)
		}
		path := d.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(vmDir, path)
		}
		disks = append(disks, vzhelper.DiskSpec{Path: path, ReadOnly: d.Readonly})
	}

	auxPath := cfg.MacOS.AuxiliaryStorage
	if auxPath != "" && !filepath.IsAbs(auxPath) {
		auxPath = filepath.Join(vmDir, auxPath)
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

	spec := &vzhelper.MachineSpec{
		Name:              cfg.Name,
		CPUs:              uint(cfg.CPUs), //nolint:gosec // guarded > 0 above; VM CPU counts are tiny
		MemoryBytes:       memBytes,
		MAC:               cfg.NIC.MAC,
		Disks:             disks,
		AuxiliaryStorage:  auxPath,
		HardwareModel:     cfg.MacOS.HardwareModel,
		MachineIdentifier: cfg.MacOS.MachineIdentifier,
		Display:           display,
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
	}, nil
}

// vzMachine implements backend.Machine by spawning a detached vee-vz-helper
// that owns the VZVirtualMachine for the VM's lifetime.
type vzMachine struct {
	manager    *Manager
	name       string
	vmDir      string
	helperPath string
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

	//nolint:gosec // helperPath is resolved from vee-managed locations, vmDir from vee storage
	cmd := exec.CommandContext(ctx, v.helperPath, "--vm-dir", v.vmDir)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setDetachAttrs(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", vzHelperBinary, err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()

	// The control socket appearing is the "VM is up" gate, mirroring the
	// QMP-socket wait on the QEMU path.
	deadline := time.Now().Add(vzStartTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			return &backend.StartResult{PID: pid, ControlSocket: sockPath}, nil
		}
		if !isAlive(pid) {
			return nil, fmt.Errorf("%s exited during startup — check %s (a common cause is a binary missing the com.apple.security.virtualization entitlement; rebuild with `make vz-helper`)", vzHelperBinary, logPath)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("%s did not open its control socket within %s — check %s", vzHelperBinary, vzStartTimeout, logPath)
}

// resolveVZHelper locates the vee-vz-helper binary: explicit override, the
// directory of the running vee binary, the vee-managed bin dir, then PATH.
func resolveVZHelper() (string, error) {
	if p := os.Getenv("VEE_VZ_HELPER"); p != "" {
		if _, err := os.Stat(p); err != nil { //nolint:gosec // deliberate operator-provided override, same trust model as QemuBinaryPath
			return "", fmt.Errorf("VEE_VZ_HELPER: %w", err)
		}
		return p, nil
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), vzHelperBinary))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".vee", "bin", vzHelperBinary))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	if p, err := exec.LookPath(vzHelperBinary); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s not found (looked in $VEE_VZ_HELPER, next to the vee binary, ~/.vee/bin, $PATH) — build and sign it with `make vz-helper`", vzHelperBinary)
}

// vzControlRequest sends one op to a helper control socket and returns the
// response.
func vzControlRequest(ctx context.Context, sockPath, op string, timeout time.Duration) (*vzhelper.Response, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "unix", sockPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	if err := json.NewEncoder(conn).Encode(&vzhelper.Request{Op: op}); err != nil {
		return nil, err
	}
	var resp vzhelper.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// watchVZShutdown blocks on the helper's wait-shutdown op and records a
// guest-initiated shutdown the same way the QMP SHUTDOWN watcher does for
// QEMU VMs. Best-effort; runs in a goroutine that outlives Start.
func (m *Manager) watchVZShutdown(ctx context.Context, name, sockPath string) {
	log := m.provider.Logger()
	// No timeout: the op blocks for the VM's lifetime, like the QMP owner
	// connection.
	resp, err := vzControlRequest(ctx, sockPath, vzhelper.OpWaitShutdown, 0)
	if err != nil {
		log.Debug("vz shutdown watcher: wait failed",
			zap.String("vm", name), zap.Error(err))
		return
	}
	if !resp.Guest {
		// Host-initiated: our own Stop recorded the reason already.
		return
	}
	m.recordGuestShutdown(name)
}
