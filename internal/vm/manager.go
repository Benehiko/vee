package vm

import (
	"context"
	"crypto/sha1" //nolint:gosec // sha1 used only to derive a stable non-cryptographic vsock CID from the VM name; changing it would break existing VMs' CIDs
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/boot"
	"github.com/Benehiko/vee/internal/cloudinit"
	cputil "github.com/Benehiko/vee/internal/cpu"
	"github.com/Benehiko/vee/internal/gpu"
	"github.com/Benehiko/vee/internal/platform"
	"github.com/Benehiko/vee/internal/qemu"
	"github.com/Benehiko/vee/internal/virtiofs"
	"github.com/Benehiko/vee/internal/virtiofsdinstall"
	"github.com/Benehiko/vee/provider"
)

type Manager struct {
	provider provider.Provider
	db       *sql.DB
	// PromptFn is called when interactive input is needed (e.g. TrueNAS password).
	// If nil, operations requiring prompts are skipped with a warning.
	PromptFn func(prompt string) (string, error)

	// qmp tracks the live QMP owner connection for each running VM in this
	// process. Only the daemon keeps a Manager alive long enough for this to
	// hold entries; short-lived CLI Managers leave it empty and fall back to
	// dialing QMP sockets directly.
	qmp *qmpRegistry
}

func NewManager(p provider.Provider) *Manager {
	return &Manager{provider: p, db: p.DB(), qmp: newQMPRegistry()}
}

func (m *Manager) storagePath() string {
	return m.provider.Config().StoragePath
}

func (m *Manager) vmDir(name string) string {
	return filepath.Join(m.storagePath(), name)
}

func (m *Manager) SaveConfig(cfg *VMConfig) error {
	return m.saveConfig(cfg)
}

func (m *Manager) LoadConfig(name string) (*VMConfig, error) {
	return m.loadConfig(name)
}

func (m *Manager) LoadState(name string) (*VMState, error) {
	return m.loadState(name)
}

func (m *Manager) SaveState(name string, state *VMState) error {
	return m.saveState(name, state)
}

func (m *Manager) saveConfig(cfg *VMConfig) error {
	if m.db != nil {
		return dbSaveConfig(m.db, cfg)
	}
	return SaveConfig(m.storagePath(), cfg)
}

func (m *Manager) loadConfig(name string) (*VMConfig, error) {
	if m.db != nil {
		return dbLoadConfig(m.db, name)
	}
	return LoadConfig(m.storagePath(), name)
}

func (m *Manager) saveState(name string, state *VMState) error {
	if m.db != nil {
		// Ensure the vms row exists before inserting state (foreign key).
		_ = dbEnsureVM(m.db, name, "")
		return dbSaveState(m.db, name, state)
	}
	return SaveStateForVM(m.storagePath(), name, state)
}

func (m *Manager) loadState(name string) (*VMState, error) {
	if m.db != nil {
		return dbLoadState(m.db, name)
	}
	return LoadState(m.storagePath(), name)
}

func (m *Manager) listAllConfigs() ([]*VMConfig, error) {
	if m.db != nil {
		return dbListAll(m.db)
	}
	return ListAll(m.storagePath())
}

// SetAutoStart toggles the auto_start flag for a VM and persists the config.
func (m *Manager) SetAutoStart(name string, enabled bool) error {
	cfg, err := m.loadConfig(name)
	if err != nil {
		return err
	}
	cfg.AutoStart = enabled
	return m.saveConfig(cfg)
}

// ListAutoStart returns configs for all VMs that have AutoStart=true.
func (m *Manager) ListAutoStart() ([]*VMConfig, error) {
	all, err := m.listAllConfigs()
	if err != nil {
		return nil, err
	}
	var out []*VMConfig
	for _, c := range all {
		if c.AutoStart {
			out = append(out, c)
		}
	}
	return out, nil
}

// Create validates and persists a new VMConfig, creating disk images and OVMF vars.
func (m *Manager) Create(ctx context.Context, cfg *VMConfig) error {
	dir := m.vmDir(cfg.Name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}

	// Warn if any disk appears to contain existing data.
	warnings, err := CheckDisksForData(cfg)
	if err != nil {
		return fmt.Errorf("disk check: %w", err)
	}
	if len(warnings) > 0 {
		fmt.Fprintln(os.Stderr, "Warning: the following disks appear to contain existing data:")
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", w.Path, w.Reason)
		}
		fmt.Fprint(os.Stderr, "Continue anyway? [y/N]: ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			return fmt.Errorf("aborted")
		}
	}

	// The aarch64 "virt" board has no legacy BIOS/CSM, so UEFI is mandatory —
	// auto-enable it regardless of the template default.
	if platform.DefaultGuestArch() == "aarch64" {
		cfg.UEFI.Enabled = true
	}

	// Copy the UEFI vars template per-VM if UEFI is requested. The provider
	// default is arch-correct (OVMF on x86_64, AAVMF/edk2-arm on aarch64).
	if cfg.UEFI.Enabled {
		src := cfg.UEFI.VarsPath
		if src == "" {
			src = m.provider.Config().OVMFVarsPath
		}
		dst := filepath.Join(dir, "OVMF_VARS.fd")
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy UEFI vars (%s): %w — UEFI firmware not found; install your distro's OVMF/edk2 package "+
				"(Arch: edk2-ovmf, Debian/Ubuntu/Mint: ovmf, Fedora: edk2-ovmf, macOS: brew install qemu), "+
				"or set ovmf_vars_path in ~/.vee/config.yaml", src, err)
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
				Owner:       wf.Owner,
				Defer:       wf.Defer,
				Encoding:    wf.Encoding,
			}
		}
		ci := &cloudinit.Config{
			Hostname:    cfg.CloudInit.Hostname,
			User:        cfg.CloudInit.User,
			Password:    cfg.CloudInit.Password,
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
		// Append cidata ISO as an extra disk entry stored in the config. On x86
		// the seed rides the IDE bus (OVMF reads it on an install boot). The
		// aarch64 "virt" machine has no IDE controller, so an ide cdrom never
		// presents to the guest and cloud-init's NoCloud datasource finds no
		// seed; attach it via virtio there (cloud-init locates the "cidata"
		// volume on any block device).
		cidataInterface := "ide"
		if platform.HostArch() == "arm64" {
			cidataInterface = "virtio"
		}
		cfg.Disks = append(cfg.Disks, DiskConfig{
			Path:       isoPath,
			Interface:  cidataInterface,
			Media:      "cdrom",
			Cache:      "none",
			Readonly:   true,
			InstallISO: true,
		})
	}

	// Assign a random free SPICE port if the template left it at 0, then
	// back-fill any ServiceEntry with Protocol=spice so tunnel can find it.
	if cfg.SPICE != nil && cfg.SPICE.Port == 0 {
		port, err := freeTCPPort()
		if err != nil {
			return fmt.Errorf("alloc SPICE port: %w", err)
		}
		cfg.SPICE.Port = port
		for i := range cfg.Services {
			if cfg.Services[i].Protocol == ServiceSPICE {
				cfg.Services[i].Port = port
			}
		}
	}

	return m.saveConfig(cfg)
}

// Start launches a VM. If foreground is true it blocks; otherwise it detaches.
func (m *Manager) Start(ctx context.Context, name string, foreground bool) error {
	cfg, err := m.loadConfig(name)
	if err != nil {
		return err
	}

	state, err := m.loadState(name)
	if err != nil {
		return err
	}
	if state.Running {
		if isAlive(state.PID) {
			return fmt.Errorf("VM %q is already running (PID %d)", name, state.PID)
		}
		// Stale state — VM shut itself down; unregister hostname and clean up.
		if cfg.Hostname != "" {
			if err := UnregisterHostname(cfg.Hostname); err != nil {
				m.provider.Logger().Warn("hostname unregistration failed",
					zap.String("hostname", cfg.Hostname), zap.Error(err))
			}
		}
		m.cleanupStaleVM(name, cfg, state)
		state = &VMState{}
	}

	// SkipInstall: treat the disk as already-installed. Strip any install ISOs
	// and mark state ready before the normal install-state logic runs.
	if cfg.SkipInstall && state.InstallState == "" {
		filtered := cfg.Disks[:0]
		for _, d := range cfg.Disks {
			if d.IsScratch() {
				m.deleteScratchDisk(cfg.Name, d)
				continue
			}
			if !d.IsInstallISO() {
				filtered = append(filtered, d)
			}
		}
		cfg.Disks = filtered
		if err := m.saveConfig(cfg); err != nil {
			return fmt.Errorf("save config (skip install): %w", err)
		}
		state.InstallState = InstallStateReady
		now := time.Now()
		state.InstalledAt = &now
		if err := m.saveState(name, state); err != nil {
			return fmt.Errorf("save state (skip install): %w", err)
		}
	}

	// Detect any one-shot installer ISO disks (InstallISO=true or media=cdrom).
	hasInstallISO := false
	for _, d := range cfg.Disks {
		if d.IsInstallISO() {
			hasInstallISO = true
			break
		}
	}

	switch state.InstallState {
	case "":
		// First boot: mark install as pending when any one-shot ISO is present.
		if hasInstallISO {
			state.InstallState = InstallStatePending
			if err := m.saveState(name, state); err != nil {
				return fmt.Errorf("save install state: %w", err)
			}
		}
	case InstallStateReady:
		// Strip installer ISO disks — they are one-shot and must not be
		// re-attached after installation completes. Persist the stripped config
		// so future starts don't see the ISOs at all. IsInstallISO also matches
		// media=cdrom disks so legacy configs (written before the install_iso
		// flag) get their now-dead installer cdrom removed here too — otherwise a
		// deleted backing ISO makes every subsequent boot fail on the missing file.
		//
		// Scratch disks (e.g. the Windows 24H2 writable install copy) are also
		// one-shot: strip them AND delete their VM-specific backing files so they
		// don't waste disk after the install completes.
		filtered := cfg.Disks[:0]
		stripped := false
		for _, d := range cfg.Disks {
			if d.IsScratch() {
				stripped = true
				m.deleteScratchDisk(cfg.Name, d)
				continue
			}
			if d.IsInstallISO() {
				stripped = true
				continue
			}
			filtered = append(filtered, d)
		}
		cfg.Disks = filtered
		if stripped {
			if err := m.saveConfig(cfg); err != nil {
				return fmt.Errorf("save config after stripping install ISOs: %w", err)
			}
		}
	}

	// Re-allocate the SPICE port if the configured port is already in use
	// (e.g. a previous run crashed without releasing the port).
	if cfg.SPICE != nil && cfg.SPICE.Port > 0 {
		if isPortInUse(cfg.SPICE.Port) {
			port, portErr := freeTCPPort()
			if portErr != nil {
				return fmt.Errorf("alloc SPICE port: %w", portErr)
			}
			cfg.SPICE.Port = port
			for i := range cfg.Services {
				if cfg.Services[i].Protocol == ServiceSPICE {
					cfg.Services[i].Port = port
				}
			}
			if err := m.saveConfig(cfg); err != nil {
				return fmt.Errorf("save config after SPICE port realloc: %w", err)
			}
		}
	}

	machine, virtiofsdPIDs, err := m.buildMachine(ctx, cfg)
	if err != nil {
		return err
	}

	if foreground {
		// Save config before launching (captures deterministic MAC etc.).
		if err := m.saveConfig(cfg); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		// Use StartDetached so we get the PID immediately and can persist
		// running state before blocking. Then poll until the process exits.
		result, err := machine.StartDetached(ctx)
		if err != nil {
			return err
		}
		fgState := &VMState{
			PID:          result.PID,
			QMPSocket:    result.QMPSocket,
			QGASocket:    result.QGASocket,
			StartedAt:    ptr(time.Now()),
			Running:      true,
			DesiredState: DesiredStateRunning,
			InstallState: state.InstallState,
			InstalledAt:  state.InstalledAt,
		}
		if cfg.SPICE != nil {
			fgState.SPICEPort = cfg.SPICE.Port
		}
		if cfg.SSHPort > 0 {
			fgState.SSHPort = cfg.SSHPort
		}
		if err := m.saveState(name, fgState); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
		// Block until QEMU exits or context is cancelled.
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				fgState.Running = false
				_ = m.saveState(name, fgState)
				return ctx.Err()
			case <-ticker.C:
				if !isAlive(result.PID) {
					fgState.Running = false
					_ = m.saveState(name, fgState)
					return nil
				}
			}
		}
	}

	// Persist any fields assigned during buildMachine (e.g. deterministic MAC).
	if err := m.saveConfig(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	result, err := machine.StartDetached(ctx)
	if err != nil {
		return err
	}

	newState := &VMState{
		PID:           result.PID,
		QMPSocket:     result.QMPSocket,
		QGASocket:     result.QGASocket,
		VirtiofsdPIDs: virtiofsdPIDs,
		StartedAt:     ptr(time.Now()),
		Running:       true,
		DesiredState:  DesiredStateRunning,
		InstallState:  state.InstallState,
		InstalledAt:   state.InstalledAt,
	}
	if cfg.SPICE != nil {
		newState.SPICEPort = cfg.SPICE.Port
	}
	if cfg.SSHPort > 0 {
		newState.SSHPort = cfg.SSHPort
	}
	if err := m.saveState(name, newState); err != nil {
		return err
	}

	// Watch for guest-initiated SHUTDOWN events so the daemon can tell
	// "user shut down inside the VM" apart from a crash. Best-effort.
	// The watcher outlives this Start call (it runs for the VM's lifetime),
	// so detach it from the request ctx — cancelling Start must not kill the
	// watcher — while still carrying any ctx values via WithoutCancel.
	if newState.QMPSocket != "" {
		go m.watchVMConnection(context.WithoutCancel(ctx), name, newState.QMPSocket)
	}

	// Register hostname → IP and inject SSH key. Best-effort: log but don't fail start.
	if cfg.Hostname != "" || cfg.Template == "truenas" {
		ip, ipErr := m.waitIP(ctx, cfg, newState, 30*time.Second)
		if ipErr != nil {
			m.provider.Logger().Warn("could not resolve VM IP",
				zap.String("vm", name), zap.Error(ipErr))
		} else {
			if cfg.Hostname != "" {
				if regErr := RegisterHostname(cfg.Hostname, ip); regErr != nil {
					m.provider.Logger().Warn("hostname registration failed",
						zap.String("hostname", cfg.Hostname), zap.Error(regErr))
				}
			}
			if cfg.Template == "truenas" {
				pubKey, keyErr := veeSSHPublicKey()
				switch {
				case keyErr != nil:
					m.provider.Logger().Warn("read vee SSH public key failed", zap.Error(keyErr))
				case m.PromptFn == nil && cfg.TrueNASAPIKey == "":
					m.provider.Logger().Warn("skipping TrueNAS SSH key injection: no API key and no prompt available")
				default:
					apiKey, adminUser, ensureErr := EnsureTrueNASAPIKey(ctx, cfg, ip, m.storagePath(), m.PromptFn)
					if ensureErr != nil {
						m.provider.Logger().Warn("TrueNAS API key setup failed", zap.Error(ensureErr))
					} else if injectErr := InjectVeeSSHKey(ctx, ip, apiKey, adminUser, pubKey); injectErr != nil {
						m.provider.Logger().Warn("TrueNAS SSH key injection failed", zap.Error(injectErr))
					}
				}
			}
		}
	}
	return nil
}

// WaitReady polls until SSH is accepting connections or QMP guest-agent responds,
// then marks the VM as ready (InstallState = "ready").
// If neither SSHPort nor QMPSocket is configured, returns immediately as long as
// the QEMU process is still alive — readiness cannot be probed without them.
// timeout is how long to wait total. Polls every 5s.
//
// WaitReady is a thin wrapper around WaitReadyWithPhases that drains the phase
// channel and returns only the terminal error. Callers that want to render a
// live phase spinner should use WaitReadyWithPhases directly.
func (m *Manager) WaitReady(ctx context.Context, name string, timeout time.Duration) error {
	phaseCh, errCh := m.WaitReadyWithPhases(ctx, name, timeout)
	for range phaseCh {
		// drain — caller doesn't want phase events
	}
	return <-errCh
}

// WaitReadyWithPhases is the same as WaitReady but also streams boot phase
// transitions on a channel. The channel is closed when readiness is reached or
// an error occurs; the error channel always receives exactly one value (nil on
// success). Phase events come from a serial-log watcher and are also persisted
// into VMState so vee status can render them out-of-band.
func (m *Manager) WaitReadyWithPhases(ctx context.Context, name string, timeout time.Duration) (<-chan boot.Event, <-chan error) {
	phaseCh := make(chan boot.Event, 16)
	errCh := make(chan error, 1)

	go func() {
		state, err := m.loadState(name)
		if err != nil {
			close(phaseCh)
			errCh <- err
			return
		}
		if !state.Running || state.PID == 0 {
			close(phaseCh)
			errCh <- fmt.Errorf("VM %q is not running", name)
			return
		}

		watchCtx, cancelWatch := context.WithCancel(ctx)

		// Phase watcher: tail serial.log and forward events both to the caller
		// and into VMState. Failures abort the readiness probe.
		watcherEvents := make(chan boot.Event, 16)
		serialLogPath := filepath.Join(m.vmDir(name), "serial.log")
		watcher := boot.NewWatcher(serialLogPath)
		// IdleTimeout slightly less than the overall WaitReady timeout so a
		// completely silent boot still gets a synthetic Failed event.
		if timeout > 30*time.Second {
			watcher.IdleTimeout = timeout - 30*time.Second
		}

		go func() {
			defer close(watcherEvents)
			_ = watcher.Run(watchCtx, watcherEvents)
		}()

		// fanout drains watcherEvents and forwards to phaseCh. It owns the
		// close(phaseCh) so the outer goroutine can never close phaseCh while
		// this one is mid-send — avoiding the "send on closed channel" race.
		failedCh := make(chan boot.Event, 1)
		var fanoutDone sync.WaitGroup
		fanoutDone.Add(1)
		go func() {
			defer fanoutDone.Done()
			defer close(phaseCh)
			for ev := range watcherEvents {
				m.persistPhase(name, ev)
				select {
				case phaseCh <- ev:
				case <-watchCtx.Done():
					// Drain remaining watcherEvents so the watcher goroutine
					// can exit; discard them since the caller is gone.
					for range watcherEvents {
					}
					return
				}
				if ev.Phase == boot.PhaseFailed {
					select {
					case failedCh <- ev:
					default:
					}
				}
			}
		}()

		// cancelWatch stops the watcher and fanout goroutines; wait for fanout
		// to finish (and close phaseCh) before this goroutine returns.
		defer func() {
			cancelWatch()
			fanoutDone.Wait()
		}()

		// No probe configured — just confirm the process is alive and return.
		// QMP socket is always present; QGA socket only when GuestAgent=true.
		if state.SSHPort == 0 && state.QGASocket == "" {
			if !isAlive(state.PID) {
				errCh <- fmt.Errorf("VM %q process (PID %d) exited immediately — check: vee logs %s", name, state.PID, name)
				return
			}
			if state.InstallState == InstallStatePending {
				errCh <- nil
				return
			}
			errCh <- m.markReady(name)
			return
		}

		deadline := time.Now().Add(timeout)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		probe := func() bool {
			if state.SSHPort > 0 {
				addr := fmt.Sprintf("127.0.0.1:%d", state.SSHPort)
				dialer := net.Dialer{Timeout: 2 * time.Second}
				conn, dialErr := dialer.DialContext(watchCtx, "tcp", addr)
				if dialErr == nil {
					_ = conn.Close()
					return true
				}
			}
			if state.QGASocket != "" {
				client, qgaErr := qemu.NewQGAClient(watchCtx, state.QGASocket, 2*time.Second)
				if qgaErr == nil {
					pingErr := client.GuestPing()
					_ = client.Close()
					if pingErr == nil {
						return true
					}
				}
			}
			return false
		}

		if probe() {
			errCh <- m.markReady(name)
			return
		}

		for {
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			case ev := <-failedCh:
				errCh <- fmt.Errorf("VM %q boot failed: %s", name, strings.TrimSpace(ev.PanicLine))
				return
			case t := <-ticker.C:
				if !isAlive(state.PID) {
					// A guest-initiated poweroff during the install pass is
					// expected — install.sh ends with `poweroff`. Treat it as
					// success so the caller can proceed to the boot pass.
					if state.InstallState == InstallStatePending && state.LastShutdownReason == ShutdownReasonGuest {
						errCh <- nil
						return
					}
					errCh <- fmt.Errorf("VM %q process (PID %d) exited — check: vee logs %s", name, state.PID, name)
					return
				}
				if t.After(deadline) {
					errCh <- fmt.Errorf("VM %q did not become ready within %s", name, timeout)
					return
				}
				if s, lerr := m.loadState(name); lerr == nil {
					state = s
				}
				if probe() {
					errCh <- m.markReady(name)
					return
				}
			}
		}
	}()

	return phaseCh, errCh
}

// persistPhase writes a phase transition into VMState. Errors are logged at
// debug level only; phase persistence is best-effort and must not block the
// boot-progress UI when the DB is briefly contended.
func (m *Manager) persistPhase(name string, ev boot.Event) {
	state, err := m.loadState(name)
	if err != nil {
		return
	}
	state.BootPhase = string(ev.Phase)
	at := ev.At
	state.PhaseStartedAt = &at
	if ev.Phase == boot.PhaseFailed {
		state.LastPanicLine = strings.TrimSpace(ev.PanicLine)
	}
	if err := m.saveState(name, state); err != nil {
		m.provider.Logger().Debug("persist boot phase",
			zap.String("vm", name), zap.String("phase", string(ev.Phase)), zap.Error(err))
	}
}

func (m *Manager) markReady(name string) error {
	state, err := m.loadState(name)
	if err != nil {
		return err
	}
	state.InstallState = InstallStateReady
	state.InstalledAt = ptr(time.Now())
	return m.saveState(name, state)
}

func ptr[T any](v T) *T { return &v }

// veeSSHPublicKey reads the vee-managed SSH public key from ~/.vee/ssh/id_ed25519.pub.
func veeSSHPublicKey() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(home, ".vee", "ssh", "id_ed25519.pub")) //nolint:gosec // fixed path under the user's home dir, not untrusted input
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// waitIP polls until the VM's IP is resolvable via MAC→ARP or QGA, up to timeout.
func (m *Manager) waitIP(ctx context.Context, cfg *VMConfig, state *VMState, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		if cfg.NIC.MAC != "" {
			if ip, err := ResolveIPFromMAC(cfg.NIC.MAC); err == nil {
				return ip, nil
			}
		}
		if state.QGASocket != "" {
			if ip, err := ResolveIPFromQGA(ctx, state.QGASocket); err == nil {
				return ip, nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("VM %q IP not resolvable within %s", cfg.Name, timeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// ResolveIPFromMAC scans the kernel neighbour table for the IP matching a MAC.
// If the MAC is not found on the first pass, it sends a broadcast ping on each
// local subnet to trigger ARP responses, then retries the neighbour table once.
func ResolveIPFromMAC(mac string) (string, error) {
	//nolint:noctx // exported helper without ctx; adding one changes its signature and all cmd/ callers
	out, err := exec.Command("ip", "neigh").Output()
	if err != nil {
		return "", fmt.Errorf("ip neigh: %w", err)
	}
	if ip, err := parseIPNeigh(string(out), mac); err == nil {
		return ip, nil
	}

	// MAC not in neighbour table — ping each local broadcast address to
	// stimulate ARP, then scan again.
	pingBroadcasts()
	//nolint:noctx // exported helper without ctx; adding one changes its signature and all cmd/ callers
	out, err = exec.Command("ip", "neigh").Output()
	if err != nil {
		return "", fmt.Errorf("ip neigh: %w", err)
	}
	return parseIPNeigh(string(out), mac)
}

// pingBroadcasts sends a single ICMP echo request to each local IPv4 broadcast
// address using a raw socket, stimulating ARP replies from guests on the subnet.
func pingBroadcasts() {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.To4() == nil {
			continue
		}
		ip4 := ipNet.IP.To4()
		mask := ipNet.Mask
		bcast := make(net.IP, 4)
		for i := range 4 {
			bcast[i] = ip4[i] | ^mask[i]
		}
		sendICMPEcho(bcast.String())
	}
}

// sendICMPEcho sends a single ICMP echo request to addr using a UDP "ping"
// socket (unprivileged on Linux 3.11+ with net.ipv4.ping_group_range set).
// Falls back silently — this is best-effort ARP stimulation only.
func sendICMPEcho(addr string) {
	//nolint:noctx // best-effort ARP stimulation; no ctx in this call chain and adding one changes exported ResolveIPFromMAC
	conn, err := net.DialTimeout("ip4:icmp", addr, time.Second)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	// Minimal ICMP echo request: type=8 code=0 checksum id=0 seq=1 data=none.
	msg := []byte{8, 0, 0, 0, 0, 1, 0, 1}
	cs := icmpChecksum(msg)
	msg[2] = byte((cs >> 8) & 0xff)
	msg[3] = byte(cs & 0xff)
	_, _ = conn.Write(msg)
}

func icmpChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 != 0 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func parseIPNeigh(output, wantMAC string) (string, error) {
	var ipv4, ipv6global string
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f != "lladdr" || i+1 >= len(fields) {
				continue
			}
			if !equalMAC(fields[i+1], wantMAC) {
				continue
			}
			ip := fields[0]
			if !strings.Contains(ip, ":") {
				// IPv4 — best choice, return immediately.
				return ip, nil
			}
			// IPv6 — skip link-local (fe80::), keep global as fallback.
			if !strings.HasPrefix(strings.ToLower(ip), "fe80") && ipv6global == "" {
				ipv6global = ip
			}
		}
	}
	if ipv4 != "" {
		return ipv4, nil
	}
	if ipv6global != "" {
		return ipv6global, nil
	}
	return "", fmt.Errorf("MAC %s not found in neighbour table", wantMAC)
}

func equalMAC(a, b string) bool {
	norm := func(s string) string {
		out := make([]byte, 0, len(s))
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c != ':' && c != '-' {
				if c >= 'A' && c <= 'F' {
					c += 32
				}
				out = append(out, c)
			}
		}
		return string(out)
	}
	return norm(a) == norm(b)
}

// ResolveIPFromQGA returns the first non-loopback IPv4 via the QEMU guest agent.
func ResolveIPFromQGA(ctx context.Context, qgaSocket string) (string, error) {
	client, err := qemu.NewQGAClient(ctx, qgaSocket, 3*time.Second)
	if err != nil {
		return "", fmt.Errorf("QGA dial: %w", err)
	}
	defer func() { _ = client.Close() }()
	ifaces, err := client.GuestNetworkGetInterfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Name == "lo" {
			continue
		}
		for _, addr := range iface.IPAddresses {
			if addr.IPAddressType == "ipv4" && addr.IPAddress != "127.0.0.1" {
				return addr.IPAddress, nil
			}
		}
	}
	return "", fmt.Errorf("no non-loopback IPv4 found via guest agent")
}

// Stop sends a graceful QMP powerdown and waits for the process to exit.
// The shutdown is recorded as user-initiated, so the daemon will NOT
// restart the VM on its next autostart pass.
func (m *Manager) Stop(ctx context.Context, name string) error {
	return m.stopWithReason(ctx, name, ShutdownReasonUser)
}

// stopWithReason is the internal stop path. The reason determines how
// DesiredState is written: ShutdownReasonUser parks the VM at
// DesiredStateStopped (the daemon honours that and won't restart it),
// whereas ShutdownReasonHost preserves DesiredStateRunning so autostart
// VMs come back up on the next host boot.
func (m *Manager) stopWithReason(ctx context.Context, name, reason string) error {
	state, err := m.loadState(name)
	if err != nil {
		return err
	}
	if !state.Running || state.PID == 0 {
		return fmt.Errorf("VM %q is not running", name)
	}

	if state.QMPSocket != "" {
		m.powerdown(ctx, name, state.QMPSocket)
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
			_ = proc.Kill()
		}
	}

	for _, pid := range state.VirtiofsdPIDs {
		if pid > 0 && isAlive(pid) {
			proc, err := os.FindProcess(pid)
			if err == nil {
				_ = terminateProcess(proc)
			}
		}
	}

	// Unregister hostname if configured.
	cfg, cfgErr := m.loadConfig(name)
	if cfgErr == nil && cfg.Hostname != "" {
		if unregErr := UnregisterHostname(cfg.Hostname); unregErr != nil {
			m.provider.Logger().Warn("hostname unregistration failed",
				zap.String("hostname", cfg.Hostname), zap.Error(unregErr))
		}
	}

	// Preserve install state across stop; pending → ready on shutdown.
	installState := state.InstallState
	if installState == InstallStatePending {
		installState = InstallStateReady
	}
	preserved := &VMState{
		InstallState:       installState,
		InstalledAt:        state.InstalledAt,
		DesiredState:       desiredStateForReason(reason),
		LastShutdownReason: reason,
	}
	return m.saveState(name, preserved)
}

// desiredStateForReason maps a shutdown reason onto the DesiredState the
// daemon should observe next boot. Host-initiated stops keep the VM marked
// "running" so autostart fires on the next cold boot; everything else
// (explicit user stop, guest poweroff) parks the VM as "stopped".
func desiredStateForReason(reason string) string {
	if reason == ShutdownReasonHost {
		return DesiredStateRunning
	}
	return DesiredStateStopped
}

// ForceStop bypasses QMP and immediately SIGKILLs the qemu process plus its
// virtiofsd helpers. Cleans up state and hostname registration the same way
// Stop does. Use only when a graceful Stop has wedged.
func (m *Manager) ForceStop(ctx context.Context, name string) error {
	return m.forceStopWithReason(ctx, name, ShutdownReasonUser)
}

func (m *Manager) forceStopWithReason(_ context.Context, name, reason string) error {
	state, err := m.loadState(name)
	if err != nil {
		return err
	}
	if !state.Running || state.PID == 0 {
		return fmt.Errorf("VM %q is not running", name)
	}

	if isAlive(state.PID) {
		if proc, err := os.FindProcess(state.PID); err == nil {
			_ = proc.Kill()
		}
	}
	for _, pid := range state.VirtiofsdPIDs {
		if pid > 0 && isAlive(pid) {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Kill()
			}
		}
	}

	if cfg, cfgErr := m.loadConfig(name); cfgErr == nil && cfg.Hostname != "" {
		if unregErr := UnregisterHostname(cfg.Hostname); unregErr != nil {
			m.provider.Logger().Warn("hostname unregistration failed",
				zap.String("hostname", cfg.Hostname), zap.Error(unregErr))
		}
	}

	installState := state.InstallState
	if installState == InstallStatePending {
		installState = InstallStateReady
	}
	preserved := &VMState{
		InstallState:       installState,
		InstalledAt:        state.InstalledAt,
		DesiredState:       desiredStateForReason(reason),
		LastShutdownReason: reason,
	}
	return m.saveState(name, preserved)
}

// StopAllRunning gracefully stops every VM that is currently Running per its
// state file. Stops run concurrently; total wall time is bounded by
// perVMTimeout regardless of VM count. Errors are logged but do not abort
// the batch — best-effort. The reason is recorded on each VM so the daemon
// can decide whether to restart autostart VMs on the next boot.
func (m *Manager) StopAllRunning(ctx context.Context, perVMTimeout time.Duration, reason string) error {
	entries, err := m.List()
	if err != nil {
		return fmt.Errorf("list VMs: %w", err)
	}

	log := m.provider.Logger()
	var running []string
	for _, e := range entries {
		if e.State != nil && e.State.Running && isAlive(e.State.PID) {
			running = append(running, e.Config.Name)
		}
	}
	if len(running) == 0 {
		return nil
	}

	log.Info("stopping all running VMs",
		zap.Int("count", len(running)),
		zap.Duration("perVMTimeout", perVMTimeout))

	errCh := make(chan error, len(running))
	var wg sync.WaitGroup
	for _, name := range running {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			stopCtx, cancel := context.WithTimeout(ctx, perVMTimeout)
			defer cancel()
			if err := m.stopWithReason(stopCtx, name, reason); err != nil {
				log.Warn("graceful stop failed during shutdown",
					zap.String("vm", name), zap.Error(err))
				errCh <- fmt.Errorf("%s: %w", name, err)
			}
		}(name)
	}
	wg.Wait()
	close(errCh)

	var errs []error
	for e := range errCh {
		errs = append(errs, e)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// cleanupStaleVM clears persisted running state for a VM whose process died on
// its own (e.g. guest OS shutdown). It does NOT touch /etc/hosts — callers
// that want hostname unregistration must call UnregisterHostname themselves.
//
// DesiredState is preserved so an explicit `vee stop` survives a daemon
// restart. LastShutdownReason is preserved if already set (e.g. by the QMP
// shutdown watcher noting a guest-initiated poweroff); otherwise the exit is
// recorded as a crash so the daemon can decide whether to restart.
func (m *Manager) cleanupStaleVM(name string, _ *VMConfig, state *VMState) {
	preserved := &VMState{}
	if state != nil {
		// A pending VM that ran long enough to shut down has completed its install.
		installState := state.InstallState
		if installState == InstallStatePending {
			installState = InstallStateReady
		}
		preserved.InstallState = installState
		preserved.InstalledAt = state.InstalledAt
		preserved.DesiredState = state.DesiredState
		preserved.LastShutdownReason = state.LastShutdownReason
		if preserved.LastShutdownReason == "" {
			preserved.LastShutdownReason = ShutdownReasonCrash
		}
	}
	_ = m.saveState(name, preserved)
}

// Delete removes a VM directory and its DB records. Refuses if the VM is running.
func (m *Manager) Delete(name string) error {
	state, err := m.loadState(name)
	if err == nil && state.Running && isAlive(state.PID) {
		return fmt.Errorf("VM %q is running; stop it first", name)
	}
	if m.db != nil {
		_ = dbDeleteVM(m.db, name)
	}
	return deleteVMDir(m.vmDir(name))
}

// deleteScratchDisk removes the backing qcow2 of a one-shot scratch disk once
// its install has completed. Best-effort: a missing file is fine, and any error
// is logged rather than failing the start (the config is stripped regardless, so
// the disk is no longer attached — a leftover file only wastes space).
//
// The path is resolved the same way qemu.Disk.AbsolutePath does: an explicit
// file path is used verbatim, an explicit directory gets the generated file name
// joined, and an empty Path resolves to the generated name relative to the
// working directory (where qemu-img created it).
func (m *Manager) deleteScratchDisk(vmName string, d DiskConfig) {
	path := scratchDiskPath(vmName, d)
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		m.provider.Logger().Warn("failed to delete scratch disk",
			zap.String("vm", vmName), zap.String("path", path), zap.Error(err))
		return
	}
	m.provider.Logger().Info("deleted install scratch disk",
		zap.String("vm", vmName), zap.String("path", path))
}

// scratchDiskPath resolves a scratch disk's backing file path, mirroring
// qemu.Disk.AbsolutePath / Disk.Name (disk-<vm>-<size>.<format>).
func scratchDiskPath(vmName string, d DiskConfig) string {
	diskSuffixes := []string{"qcow2", "qcow", "img", "raw", "iso", "vmdk", "vdi", "vhd"}
	for _, suffix := range diskSuffixes {
		if strings.HasSuffix(d.Path, suffix) {
			return d.Path
		}
	}
	format := d.Format
	if format == "" {
		format = "qcow2"
	}
	name := fmt.Sprintf("disk-%s-%s.%s", vmName, d.Size, format)
	return filepath.Join(d.Path, name)
}

// deleteVMDir removes all contents of dir except the backups/ subdirectory.
// If no backups exist the directory itself is removed; otherwise it is kept so
// backups remain accessible at their original path.
func deleteVMDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	hasBackups := false
	for _, e := range entries {
		if e.Name() == "backups" && e.IsDir() {
			hasBackups = true
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}

	if !hasBackups {
		return os.Remove(dir)
	}
	return nil
}

type ListEntry struct {
	Config *VMConfig
	State  *VMState
}

// List returns all VMs with their current state.
func (m *Manager) List() ([]*ListEntry, error) {
	configs, err := m.listAllConfigs()
	if err != nil {
		return nil, err
	}
	var entries []*ListEntry
	for _, cfg := range configs {
		state, _ := m.loadState(cfg.Name)
		if state == nil {
			state = &VMState{}
		}
		// Verify the PID is still alive; clean up if VM shut itself down.
		if state.Running && !isAlive(state.PID) {
			m.cleanupStaleVM(cfg.Name, cfg, state)
			state.Running = false
		}
		entries = append(entries, &ListEntry{Config: cfg, State: state})
	}
	return entries, nil
}

// buildMachine constructs a BaseMachine from a VMConfig, starting virtiofsd if needed.
func (m *Manager) buildMachine(ctx context.Context, cfg *VMConfig) (*qemu.BaseMachine, []int, error) {
	machine, err := qemu.NewEmptyMachine(m.provider)
	if err != nil {
		return nil, nil, err
	}

	opts := []qemu.QemuOptions{
		qemu.WithName(cfg.Name),
		qemu.WithMemory(cfg.Memory),
	}

	// CPU — gaming passthrough merges GamingCPUFlags before building.
	guestArch := platform.DefaultGuestArch()
	cpuModel := cfg.CPUModel
	cpuFlags := cfg.CPUFlags
	if cfg.GPU.Mode == GPUPassthrough && cfg.GPU.AntiDetect {
		cpuFlags = append(cpuFlags, qemu.GamingCPUFlags...)
	}
	if guestArch == "aarch64" {
		// The x86 CPU model catalogue and hyperv/anti-detect flags are invalid
		// on aarch64; HVF only supports host passthrough. Force -cpu host and
		// drop x86-specific flags.
		if cpuModel != "host" && cpuModel != "max" {
			cpuModel = "host"
		}
		if len(cpuFlags) > 0 {
			m.provider.Logger().Warn("dropping x86-specific CPU flags on aarch64 guest",
				zap.String("vm", cfg.Name),
				zap.Strings("flags", cpuFlags))
			cpuFlags = nil
		}
	}
	sockets, cores, threads := cputil.AdjustSMP(cfg.CPUs, cfg.Sockets, cfg.Cores, cfg.Threads)
	cpu := qemu.NewCPU(m.provider,
		qemu.WithCPUModel(qemu.CPUModel(cpuModel)),
		qemu.WithSMP(cfg.CPUs, sockets, threads, cores),
		qemu.WithCPUFlags(cpuFlags),
	)
	opts = append(opts, qemu.WithCPU(cpu))

	// Machine type override (e.g. "q35,smm=on" for Windows Secure Boot) and any
	// extra -global args (e.g. the secure-pflash global).
	opts = append(opts, qemu.WithMachineType(cfg.MachineType))
	for _, g := range cfg.Globals {
		opts = append(opts, qemu.WithGlobal(g))
	}

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
	if cfg.SSHPort > 0 {
		port := availablePort(cfg.SSHPort, 2200, 2299)
		cfg.SSHPort = port
		nicHostFwds = append(nicHostFwds, fmt.Sprintf("tcp:127.0.0.1:%d-:22", port))
	}
	nicMode := cfg.NIC.Mode
	if nicMode == "bridge" && !platform.SupportsBridgeNetworking() {
		m.provider.Logger().Warn("bridge networking is unsupported on this host (Linux only) — falling back to user-mode NAT",
			zap.String("vm", cfg.Name),
			zap.String("host_os", platform.HostOS()))
		nicMode = "user"
	}
	nic := qemu.NewNIC(qemu.NICMode(nicMode), cfg.NIC.Bridge, cfg.NIC.MAC, nicHostFwds...)
	if nicMode == "bridge" && cfg.CPUs > 1 {
		helperPath := m.provider.Config().BridgeHelperPath
		if helperPath != "" {
			if _, statErr := os.Stat(helperPath); statErr == nil {
				nic.Queues = min(cfg.CPUs, 8)
				nic.BridgeHelper = helperPath
			} else {
				m.provider.Logger().Warn("qemu-bridge-helper not found, multiqueue disabled",
					zap.String("path", helperPath))
			}
		}
	}
	opts = append(opts, qemu.WithNIC(nic))

	// Disks
	for i, d := range cfg.Disks {
		// A cdrom installer whose backing ISO no longer exists must not be
		// attached: QEMU hard-fails at open() and the VM can never boot. This is
		// the last line of defence — the install-state machine strips such disks
		// once install completes — but a stale config (e.g. a manually-restored
		// vm.yaml, or a still-pending install with a deleted ISO) can slip a
		// missing cdrom through to here. Skip it with a warning rather than
		// aborting the boot of an already-installed guest.
		if d.Media == "cdrom" && d.Path != "" {
			if _, statErr := os.Stat(d.Path); os.IsNotExist(statErr) {
				m.provider.Logger().Warn("skipping cdrom disk: backing file is missing",
					zap.String("vm", cfg.Name), zap.String("path", d.Path))
				continue
			}
		}
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
			qemu.WithBootIndex(d.BootIndex),
		)
		_ = i
		opts = append(opts, qemu.AddDisk(disk))
	}

	// GPU
	// Tracks whether a shared-memory backend (memory-backend-memfd,share=on)
	// has been added. Both VFIO passthrough and virtiofs use the vhost-user
	// protocol, which requires guest RAM to live in a shareable backing so the
	// helper process can map it. Passthrough adds it below; virtiofs mounts on
	// non-passthrough VMs (e.g. the windows template) must add it themselves.
	memfdAdded := false
	switch cfg.GPU.Mode {
	case GPUPassthrough:
		// VFIO PCI passthrough is a Linux kernel feature with no macOS
		// equivalent; refuse rather than emit a vfio-pci device that cannot bind.
		if !platform.SupportsVFIO() {
			return nil, nil, fmt.Errorf("GPU passthrough (gpu.mode=passthrough) is only supported on Linux hosts; "+
				"on %s use gpu.mode=virtio for accelerated virtio-gpu instead", platform.HostOS())
		}
		if cfg.GPU.PCIAddr != "" {
			pf := gpu.PreflightCheck(cfg.GPU.PCIAddr, cfg.Memory)
			fields := []zap.Field{
				zap.String("pci_addr", pf.PCIAddr),
				zap.String("driver", pf.Driver),
				zap.Int("iommu_group", pf.IOMMUGroup),
				zap.String("vfio_dev", pf.VFIODevPath),
				zap.Bool("vfio_accessible", pf.VFIOAccessible),
				zap.String("memlock_soft", gpu.FormatBytes(pf.MemlockSoftBytes)),
				zap.String("memlock_hard", gpu.FormatBytes(pf.MemlockHardBytes)),
				zap.String("memlock_required", gpu.FormatBytes(pf.MemlockRequiredBytes)),
				zap.Bool("memlock_ok", pf.MemlockOK()),
			}
			for key, err := range pf.Errors {
				fields = append(fields, zap.String("preflight_fail_"+key, err.Error()))
			}
			if pf.OK() {
				m.provider.Logger().Info("VFIO preflight passed", fields...)
			} else {
				m.provider.Logger().Warn("VFIO preflight issues detected — passthrough may fail", fields...)
			}
			for _, peer := range pf.GroupPeers {
				m.provider.Logger().Info("IOMMU group peer",
					zap.String("pci_addr", peer.Address),
					zap.String("driver", peer.Driver),
					zap.Bool("is_gpu", peer.IsGPU),
				)
			}
			// Ensure all VFIO devices are in a clean power state before handing
			// them to QEMU. An unclean previous exit can leave the GPU in D3cold,
			// which causes "error getting device from group N: No such device".
			// D3hot/suspended is handled by vfio-pci during open — warn only.
			allAddrs := append([]string{cfg.GPU.PCIAddr}, cfg.GPU.ExtraVFIOAddrs...)
			var stuck []string
			for _, addr := range allAddrs {
				ds, resetErr := gpu.EnsureReady(addr)
				switch {
				case resetErr != nil:
					m.provider.Logger().Warn("VFIO device wake/reset failed",
						zap.String("pci_addr", addr),
						zap.String("power_state", string(ds.PowerState)),
						zap.String("runtime_status", ds.RuntimeStatus),
						zap.Error(resetErr),
					)
					if ds.NeedsReset() {
						stuck = append(stuck, fmt.Sprintf("%s (%s/%s)", addr, ds.PowerState, ds.RuntimeStatus))
					}
				case ds.NeedsAttention():
					m.provider.Logger().Warn("VFIO device in D3hot/suspended — vfio-pci will attempt runtime resume",
						zap.String("pci_addr", addr),
						zap.String("power_state", string(ds.PowerState)),
						zap.String("runtime_status", ds.RuntimeStatus),
					)
				default:
					m.provider.Logger().Info("VFIO device ready",
						zap.String("pci_addr", addr),
						zap.String("power_state", string(ds.PowerState)),
						zap.String("runtime_status", ds.RuntimeStatus),
					)
				}
			}
			// QEMU asserts immediately (pci_irq_handler) when asked to attach
			// a device stuck in D3cold, so refuse to launch instead of
			// crashing into a confusing core dump. Wake requires writing
			// /sys/bus/pci/.../power/control which is root-only on most
			// kernels — installing the daemon polkit rules grants vee that
			// access. Past that, only a host cold reboot can revive a GPU
			// that lost power across an unclean exit.
			if len(stuck) > 0 {
				return nil, nil, fmt.Errorf(
					"VFIO device(s) stuck in D3cold: %s — fix with one of: "+
						"(1) sudo vee daemon install (one-time polkit setup so vee can wake the device), "+
						"(2) sudo bash -c 'echo on > /sys/bus/pci/devices/<addr>/power/control' for each address, "+
						"(3) cold reboot the host",
					strings.Join(stuck, ", "))
			}

			// Soft-reset AMD GPUs that lack FLR by cycling through the native
			// driver before handing back to vfio-pci. This is a best-effort
			// workaround for Navi31/RDNA3 and similar hardware; only the
			// primary GPU address is cycled (the audio sibling shares the same
			// reset domain and doesn't need a separate cycle).
			if cfg.GPU.RebindReset && cfg.GPU.RebindResetDriver != "" {
				m.provider.Logger().Info("performing driver rebind reset",
					zap.String("pci_addr", cfg.GPU.PCIAddr),
					zap.String("native_driver", cfg.GPU.RebindResetDriver),
				)
				if err := gpu.RebindReset(cfg.GPU.PCIAddr, cfg.GPU.RebindResetDriver); err != nil {
					m.provider.Logger().Warn("driver rebind reset failed — continuing anyway",
						zap.String("pci_addr", cfg.GPU.PCIAddr),
						zap.Error(err),
					)
				} else {
					m.provider.Logger().Info("driver rebind reset complete",
						zap.String("pci_addr", cfg.GPU.PCIAddr),
					)
				}
			}

			primary := qemu.NewVFIODevice(cfg.GPU.PCIAddr)
			if cfg.GPU.ROMBar || cfg.GPU.ROMFile != "" {
				primary.ROMBar = true
			}
			if cfg.GPU.ROMFile != "" {
				primary.ROMFile = cfg.GPU.ROMFile
			}
			opts = append(opts, qemu.WithVFIO(primary))
			// Peer devices fall into two buckets. If the peer is a sibling
			// PCI function of the primary (same domain:bus:slot — e.g. a
			// GPU's HDMI/DP audio at xx:yy.1 next to the VGA at xx:yy.0),
			// it MUST share the primary's pcie-root-port and sit at the
			// matching function number, otherwise QEMU's pci_irq_handler
			// asserts at boot. Unrelated peers each get their own port.
			nextSlot := 2
			for _, addr := range cfg.GPU.ExtraVFIOAddrs {
				if qemu.SameSlot(cfg.GPU.PCIAddr, addr) {
					fn := qemu.FunctionNumber(addr)
					sibling := qemu.NewVFIOSiblingFunction(addr, primary.BusID, fn)
					opts = append(opts, qemu.WithVFIO(sibling))
					m.provider.Logger().Info("attaching VFIO sibling function",
						zap.String("pci_addr", addr),
						zap.String("bus_id", primary.BusID),
						zap.Int("guest_function", fn),
					)
					continue
				}
				busID := fmt.Sprintf("pcie.%d", nextSlot)
				peer := qemu.NewVFIOPeerDevice(addr, busID, nextSlot)
				opts = append(opts, qemu.WithVFIO(peer))
				m.provider.Logger().Info("attaching VFIO peer device",
					zap.String("pci_addr", addr),
					zap.String("bus_id", busID),
				)
				nextSlot++
			}
		} else {
			m.provider.Logger().Warn("GPU mode is passthrough but no pci_addr configured — no VFIO device will be attached")
		}
		// Passthrough VMs need shared memory for the vhost-user protocol.
		opts = append(opts, qemu.WithMemfd(qemu.NewMemfdBackend(cfg.Memory)))
		memfdAdded = true
	case GPUVirtio:
		opts = append(opts, qemu.WithVGA("none"))
		arch := platform.DefaultGuestArch()
		if cfg.Headless || cfg.SPICE != nil {
			// The GL-capable virtio-gpu device requires a windowed display with
			// a host GL context; headless and SPICE VMs use -display none which
			// provides none. Fall back to a plain (2D) virtio-gpu adapter.
			opts = append(opts, qemu.WithDevice(qemu.VirtioGPUDevice(arch, false, false, "")))
		} else {
			// Host- and arch-aware GL adapter + display. On macOS this is
			// virtio-gpu-gl-pci + "-display cocoa,gl=es" (ANGLE/Metal); on Linux
			// virtio-vga-gl + "-display gtk,gl=on". Hardware acceleration only
			// materializes when the resolved QEMU was built with virglrenderer.
			dev := qemu.VirtioGPUDevice(arch, true, cfg.GPU.Venus, cfg.GPU.HostMem)
			opts = append(opts, qemu.WithDevice(dev))
			opts = append(opts, qemu.WithDisplay(qemu.DisplayArg(platform.HostOS(), true, qemu.GLBackend(cfg.GPU.GLBackend))))
			if platform.IsMacOS() {
				m.provider.Logger().Info("virtio-gpu GL enabled — accelerated only with a virglrenderer-capable QEMU (vee-qemu, UTM, or a qemu-virgl tap); stock/Homebrew QEMU renders in software",
					zap.String("vm", cfg.Name),
					zap.String("device", dev))
			}
			if cfg.GPU.Venus {
				m.provider.Logger().Warn("Venus (Vulkan-over-virtio) is experimental; desktop Vulkan compositing is unreliable — prefer virgl OpenGL for the desktop",
					zap.String("vm", cfg.Name))
			}
		}
	case GPUAppleGFX:
		// apple-gfx (ParavirtualizedGraphics.framework) accelerates macOS guests
		// only and needs the vmapple machine, AVPBooter firmware, and a
		// code-signed binary. The device/machine building blocks exist
		// (qemu.AppleGFXDevice / qemu.VMAppleMachineType); full template wiring
		// is pending, so refuse clearly rather than emit a half-configured VM.
		if !platform.IsMacOS() {
			return nil, nil, fmt.Errorf("GPU mode apple-gfx requires a macOS host (got %s)", platform.HostOS())
		}
		return nil, nil, fmt.Errorf("GPU mode apple-gfx is not yet wired end-to-end (requires vmapple machine + AVPBooter); " +
			"use gpu.mode=virtio for Linux guests in the meantime")
	}

	// Explicit VGA override (e.g. "none" for passthrough VMs using virtio-gpu-pci via ExtraDevices).
	if cfg.VGA != "" {
		opts = append(opts, qemu.WithVGA(cfg.VGA))
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

	// QMP control channel (unix socket on unix hosts, loopback TCP on Windows).
	qmpSock, err := controlSocketAddr(m.vmDir(cfg.Name), "qmp")
	if err != nil {
		return nil, nil, err
	}
	opts = append(opts, qemu.WithQMPSocket(qmpSock))

	// QGA control channel (guest agent), same transport rules as QMP.
	if cfg.GuestAgent {
		qgaSock, qgaErr := controlSocketAddr(m.vmDir(cfg.Name), "qga")
		if qgaErr != nil {
			return nil, nil, qgaErr
		}
		opts = append(opts, qemu.WithQGASocket(qgaSock))
	}

	// Extra devices (e.g. virtio-serial-pci for guest agent)
	for _, dev := range cfg.ExtraDevices {
		opts = append(opts, qemu.WithDevice(dev))
	}

	// Virtiofsd mounts — ensure the binary exists before starting any daemon.
	// virtiofsd is Linux only; on other hosts skip mounts with a warning rather
	// than emitting a vhost-user-fs device that cannot be backed.
	var virtiofsdPIDs []int
	virtiofsMounts := cfg.VirtiofsMounts
	if len(virtiofsMounts) > 0 && !platform.SupportsVirtiofsd() {
		m.provider.Logger().Warn("virtiofs shares are unsupported on this host (virtiofsd is Linux only) — skipping",
			zap.String("vm", cfg.Name),
			zap.Int("mounts", len(virtiofsMounts)),
			zap.String("host_os", platform.HostOS()))
		virtiofsMounts = nil
	}
	if len(virtiofsMounts) > 0 {
		// virtiofs uses vhost-user, which needs guest RAM in a shareable
		// backing. Passthrough already added the memfd backend; add it here for
		// non-passthrough VMs (e.g. the windows template) so the share works.
		if !memfdAdded {
			opts = append(opts, qemu.WithMemfd(qemu.NewMemfdBackend(cfg.Memory)))
		}
		home, homeErr := os.UserHomeDir()
		if homeErr == nil {
			//nolint:contextcheck // EnsureVirtiofsd lives in internal/virtiofsdinstall and takes no ctx; adding one is out of scope for this package
			path, ensureErr := virtiofsdinstall.EnsureVirtiofsd(home)
			if ensureErr != nil && errors.Is(ensureErr, virtiofsdinstall.ErrNoContainerRuntime) {
				// No host container runtime — fall back to compiling inside a
				// throwaway VM.
				path, ensureErr = m.buildVirtiofsdInVM(ctx, home)
			}
			if ensureErr == nil {
				m.provider.Config().VirtiofsdPath = path
			} else {
				m.provider.Logger().Warn("virtiofsd not available", zap.Error(ensureErr))
			}
		}
	}
	for _, mount := range virtiofsMounts {
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
			virtiofsdPIDs = append(virtiofsdPIDs, pid)
			// Wait for virtiofsd to create its socket before QEMU starts.
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if _, statErr := os.Stat(sockPath); statErr == nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		opts = append(opts, qemu.WithVirtiofsd(sockPath, mount.Tag))
	}

	// TPM — start swtpm daemon before QEMU. swtpm wiring is Linux-only in vee;
	// on other hosts warn and continue without a TPM rather than aborting.
	if cfg.TPM != nil && cfg.TPM.Enabled && !platform.SupportsSwTPM() {
		m.provider.Logger().Warn("software TPM (swtpm) is unsupported on this host — continuing without a TPM",
			zap.String("vm", cfg.Name),
			zap.String("host_os", platform.HostOS()))
	} else if cfg.TPM != nil && cfg.TPM.Enabled {
		tpmDir := filepath.Join(m.vmDir(cfg.Name), "tpm")
		tpmSock := filepath.Join(m.vmDir(cfg.Name), "tpm.sock")
		if err := startSwtpm(tpmDir, tpmSock); err != nil {
			return nil, nil, fmt.Errorf("swtpm: %w", err)
		}
		opts = append(opts, qemu.WithTPM(qemu.NewTPM(tpmSock)))
	}

	// VSock — add vhost-vsock-pci device when SSH sharing is enabled. vhost is
	// Linux-only; on other hosts SSH sharing relies on user-mode port forwarding.
	if cfg.SSHShare && !platform.SupportsVsock() {
		m.provider.Logger().Warn("vsock (vhost-vsock-pci) is unsupported on this host — SSH share falls back to user-mode port forwarding",
			zap.String("vm", cfg.Name),
			zap.String("host_os", platform.HostOS()))
	} else if cfg.SSHShare {
		cid := cfg.VsockCID
		if cid == 0 {
			cid = deterministicCID(cfg.Name)
		}
		opts = append(opts, qemu.WithVSock(qemu.NewVSockDevice(cid)))
	}

	// CPU pinning — applied post-launch via taskset on vCPU threads.
	if len(cfg.CPUPinning) > 0 {
		opts = append(opts, qemu.WithCPUPinning(cfg.CPUPinning))
	}

	// RTC
	if cfg.RTC != "" {
		opts = append(opts, qemu.WithRTC(cfg.RTC))
	}

	// Force disk-first boot order for installed VMs so UEFI doesn't waste
	// time on PXE before finding the GRUB EFI entry on the disk.
	state, stateErr := m.loadState(cfg.Name)
	if stateErr == nil && state.InstallState == InstallStateReady {
		opts = append(opts, qemu.WithBootOrder("c"))
	}

	built, err := machine.BuildMachine(opts...)
	if err != nil {
		return nil, nil, err
	}

	return built, virtiofsdPIDs, nil
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
	//nolint:gosec,noctx // swtpm is a fixed binary; args are vee-controlled VM paths; cmd is returned for the caller to run, so no ctx is threaded here
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
	h := sha1.Sum([]byte(name)) //nolint:gosec // non-cryptographic: stable CID derivation from VM name
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
	//nolint:noctx // local port-availability probe; no ctx available and adding one requires an API change across callers
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
	return processAlive(pid)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // src is a vee-controlled VM file path, not untrusted input
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst) //nolint:gosec // dst is a vee-controlled VM file path, not untrusted input
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, in)
	return err
}

// RunHealthCheck runs /usr/local/bin/vee-check inside the VM, parses its JSON
// output, and persists the results into VMState. The VM must be running.
func (m *Manager) RunHealthCheck(ctx context.Context, name string) ([]HealthCheck, error) {
	cfg, err := m.loadConfig(name)
	if err != nil {
		return nil, err
	}
	state, err := m.loadState(name)
	if err != nil {
		return nil, err
	}
	if !state.Running {
		return nil, fmt.Errorf("VM %q is not running", name)
	}

	out, err := m.runCheckScript(ctx, cfg, state)
	if err != nil {
		return nil, fmt.Errorf("run vee-check: %w", err)
	}

	var result struct {
		Checks []HealthCheck `json:"checks"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return nil, fmt.Errorf("parse vee-check output: %w\noutput: %s", err, out)
	}

	now := time.Now()
	state.PostInstallChecks = result.Checks
	state.PostInstallCheckedAt = &now
	if saveErr := m.saveState(name, state); saveErr != nil {
		return nil, fmt.Errorf("persist health checks: %w", saveErr)
	}
	return result.Checks, nil
}

// runCheckScript executes /usr/local/bin/vee-check in the VM and returns stdout.
// Prefers QGA (works without network routing); falls back to SSH port.
func (m *Manager) runCheckScript(ctx context.Context, cfg *VMConfig, state *VMState) (string, error) {
	if state.QGASocket != "" {
		client, err := qemu.NewQGAClient(ctx, state.QGASocket, 5*time.Second)
		if err != nil {
			return "", fmt.Errorf("connect QGA socket %s: %w", state.QGASocket, err)
		}
		defer func() { _ = client.Close() }()
		out, stderr, exitCode, runErr := client.RunCommand("/usr/local/bin/vee-check", nil)
		if runErr != nil {
			return "", fmt.Errorf("QGA exec /usr/local/bin/vee-check: %w", runErr)
		}
		if exitCode != 0 {
			detail := strings.TrimSpace(stderr)
			if detail == "" {
				detail = strings.TrimSpace(out)
			}
			return "", fmt.Errorf("/usr/local/bin/vee-check exited %d: %s", exitCode, detail)
		}
		return strings.TrimSpace(out), nil
	}
	if state.SSHPort > 0 {
		home, _ := os.UserHomeDir()
		identity := home + "/.vee/ssh/id_ed25519"
		user := cfg.SSHUser
		if user == "" && cfg.CloudInit != nil {
			user = cfg.CloudInit.DefaultUser
		}
		if user == "" {
			user = "root"
		}
		args := []string{
			"-i", identity,
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "ConnectTimeout=5",
			"-o", "LogLevel=ERROR",
			"-p", fmt.Sprintf("%d", state.SSHPort),
			fmt.Sprintf("%s@127.0.0.1", user),
			"/usr/local/bin/vee-check",
		}
		out, execErr := exec.CommandContext(ctx, "ssh", args...).Output() //nolint:gosec // ssh binary is fixed; args are vee-controlled identity/port/user for the managed VM
		if execErr != nil {
			return "", fmt.Errorf("ssh exec: %w", execErr)
		}
		return strings.TrimSpace(string(out)), nil
	}
	return "", fmt.Errorf("no QGA socket or SSH port available for health check")
}
