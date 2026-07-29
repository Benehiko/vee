package vm

import (
	"context"
	"encoding/json"
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
	"github.com/Benehiko/vee/internal/vzhelper"
)

// vzHelperBinary aliases the shared helper name (resolution lives in
// vzhelper.ResolveHelper so images/templates can reuse it).
const vzHelperBinary = vzhelper.HelperBinary

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
	if cfg.SSHPort > 0 {
		return nil, fmt.Errorf("the vz backend does not support ssh_port: NAT has no host port-forwarding — remove ssh_port and use `vee ssh` (resolves the guest IP by MAC)")
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
	// Enforce the restored image's recorded minimums at start too — configs
	// can be hand-edited below them, and a guest under the minimums will
	// not boot.
	cpus := uint64(cfg.CPUs) //nolint:gosec // guarded > 0 above
	if cfg.MacOS.MinCPUs > 0 && cpus < cfg.MacOS.MinCPUs {
		cpus = cfg.MacOS.MinCPUs
	}
	if cfg.MacOS.MinMemoryBytes > 0 && memBytes < cfg.MacOS.MinMemoryBytes {
		memBytes = cfg.MacOS.MinMemoryBytes
	}

	// Paths must be absolute: vee's other disk consumers (data-presence
	// checks, boot-disk moves) resolve relative paths against the process
	// CWD, so accepting them here would give one field two meanings.
	disks := make([]vzhelper.DiskSpec, 0, len(cfg.Disks))
	for _, d := range cfg.Disks {
		if d.Format != "" && d.Format != "raw" {
			return nil, fmt.Errorf("the vz backend supports raw disk images only (disk %s has format %q)", d.Path, d.Format)
		}
		if !filepath.IsAbs(d.Path) {
			return nil, fmt.Errorf("the vz backend requires absolute disk paths (got %q)", d.Path)
		}
		disks = append(disks, vzhelper.DiskSpec{Path: d.Path, ReadOnly: d.Readonly})
	}
	auxPath := cfg.MacOS.AuxiliaryStorage
	if auxPath != "" && !filepath.IsAbs(auxPath) {
		return nil, fmt.Errorf("the vz backend requires an absolute auxiliary_storage path (got %q)", auxPath)
	}

	// ResolveHelper also heals a quarantined helper; a failure there is fatal
	// because Gatekeeper would SIGKILL the helper on exec.
	helperPath, err := vzhelper.ResolveHelper()
	if err != nil {
		return nil, err
	}

	vmDir := m.vmDir(cfg.Name)

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
		CPUs:              uint(cpus), //nolint:gosec // VM CPU counts are tiny
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
		mac:        cfg.NIC.MAC,
	}, nil
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

// waitReadyVZ polls until the vz guest is reachable: readiness means the
// guest acquired a FRESH DHCP lease. bootpd keeps stale entries in
// /var/db/dhcpd_leases across shutdowns (even past expiry), so a bare MAC
// match proves nothing on a restart — instead the lease expiry recorded for
// the MAC must ADVANCE past the baseline taken before the VM started (every
// DHCP grant/renewal rewrites it). A fresh macOS guest has no SSH enabled
// yet, so the lease is the strongest "the guest is up" signal available.
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
		return exp > 0 && exp > baseline
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

// watchVZShutdown blocks on the helper's wait-shutdown op and records a
// guest-initiated shutdown the same way the QMP SHUTDOWN watcher does for
// QEMU VMs. Best-effort; runs in a goroutine that outlives Start.
func (m *Manager) watchVZShutdown(ctx context.Context, name, sockPath string) {
	log := m.provider.Logger()
	// No timeout: the op blocks for the VM's lifetime, like the QMP owner
	// connection.
	resp, err := vzControlRequest(ctx, sockPath, vzhelper.OpWaitShutdown, 0)
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

// vzShutdownOverSSH asks a macOS guest to power itself off. macOS ignores
// VZVirtualMachine.requestStop — the ACPI-powerdown analog — so without this
// every `vee stop` waits out its grace period and then SIGKILLs the VM, which
// leaves the guest filesystem unclean. Provisioning installs a sudoers rule
// granting exactly /sbin/shutdown, so no password is needed.
func (m *Manager) vzShutdownOverSSH(ctx context.Context, name string) error {
	cfg, err := m.loadConfig(name)
	if err != nil {
		return err
	}
	if cfg.SSHUser == "" {
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
	out, err := exec.CommandContext(shutdownCtx, sshBin, vzShutdownArgs(cfg.SSHUser, ip, home)...).CombinedOutput()
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
