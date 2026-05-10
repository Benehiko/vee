package vm

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Benehiko/vee/internal/cloudinit"
	cputil "github.com/Benehiko/vee/internal/cpu"
	"github.com/Benehiko/vee/internal/gpu"
	"github.com/Benehiko/vee/internal/qemu"
	"github.com/Benehiko/vee/internal/virtiofs"
	"github.com/Benehiko/vee/internal/virtiofsdinstall"
	"github.com/Benehiko/vee/provider"
	"go.uber.org/zap"
)

type Manager struct {
	provider provider.Provider
	db       *sql.DB
	// PromptFn is called when interactive input is needed (e.g. TrueNAS password).
	// If nil, operations requiring prompts are skipped with a warning.
	PromptFn func(prompt string) (string, error)
}

func NewManager(p provider.Provider) *Manager {
	return &Manager{provider: p, db: p.DB()}
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
			Path:       isoPath,
			Interface:  "virtio",
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

	// Detect any one-shot installer ISO disks (InstallISO=true).
	hasInstallISO := false
	for _, d := range cfg.Disks {
		if d.InstallISO {
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
		// Strip InstallISO disks — they are one-shot and must not be
		// re-attached after installation completes. Persist the stripped config
		// so future starts don't see the ISOs at all.
		filtered := cfg.Disks[:0]
		for _, d := range cfg.Disks {
			if d.InstallISO {
				continue
			}
			filtered = append(filtered, d)
		}
		cfg.Disks = filtered
		if err := m.saveConfig(cfg); err != nil {
			return fmt.Errorf("save config after stripping install ISOs: %w", err)
		}
	}

	machine, virtiofsdPIDs, err := m.buildMachine(ctx, cfg)
	if err != nil {
		return err
	}

	if foreground {
		return machine.Start(ctx)
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
				if keyErr != nil {
					m.provider.Logger().Warn("read vee SSH public key failed", zap.Error(keyErr))
				} else if m.PromptFn == nil && cfg.TrueNASAPIKey == "" {
					m.provider.Logger().Warn("skipping TrueNAS SSH key injection: no API key and no prompt available")
				} else {
					apiKey, adminUser, ensureErr := EnsureTrueNASAPIKey(cfg, ip, m.storagePath(), m.PromptFn)
					if ensureErr != nil {
						m.provider.Logger().Warn("TrueNAS API key setup failed", zap.Error(ensureErr))
					} else if injectErr := InjectVeeSSHKey(ip, apiKey, adminUser, pubKey); injectErr != nil {
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
func (m *Manager) WaitReady(ctx context.Context, name string, timeout time.Duration) error {
	state, err := m.loadState(name)
	if err != nil {
		return err
	}
	if !state.Running || state.PID == 0 {
		return fmt.Errorf("VM %q is not running", name)
	}

	// No probe configured — just confirm the process is alive and return.
	// QMP socket is always present; QGA socket only when GuestAgent=true.
	if state.SSHPort == 0 && state.QGASocket == "" {
		if !isAlive(state.PID) {
			return fmt.Errorf("VM %q process (PID %d) exited immediately — check: vee logs %s", name, state.PID, name)
		}
		// Don't mark ready during a first-boot install — the VM is doing its
		// install run and will shut itself down when done. Marking ready here
		// would cause the next start to strip the install ISO prematurely.
		if state.InstallState == InstallStatePending {
			return nil
		}
		return m.markReady(name)
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

		// QGA guest-ping probe — requires GuestAgent=true in the VM config.
		if state.QGASocket != "" {
			client, qgaErr := qemu.NewQGAClient(state.QGASocket, 2*time.Second)
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

	// Check immediately before first tick.
	if probe() {
		return m.markReady(name)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-ticker.C:
			if !isAlive(state.PID) {
				return fmt.Errorf("VM %q process (PID %d) exited — check: vee logs %s", name, state.PID, name)
			}
			if t.After(deadline) {
				return fmt.Errorf("VM %q did not become ready within %s", name, timeout)
			}
			// Reload state in case SSH port changed.
			if s, lerr := m.loadState(name); lerr == nil {
				state = s
			}
			if probe() {
				return m.markReady(name)
			}
		}
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
	data, err := os.ReadFile(filepath.Join(home, ".vee", "ssh", "id_ed25519.pub"))
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
			if ip, err := ResolveIPFromQGA(state.QGASocket); err == nil {
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
func ResolveIPFromMAC(mac string) (string, error) {
	out, err := exec.Command("ip", "neigh").Output()
	if err != nil {
		return "", fmt.Errorf("ip neigh: %w", err)
	}
	return parseIPNeigh(string(out), mac)
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
func ResolveIPFromQGA(qgaSocket string) (string, error) {
	client, err := qemu.NewQGAClient(qgaSocket, 3*time.Second)
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
func (m *Manager) Stop(ctx context.Context, name string) error {
	state, err := m.loadState(name)
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

	for _, pid := range state.VirtiofsdPIDs {
		if pid > 0 && isAlive(pid) {
			proc, err := os.FindProcess(pid)
			if err == nil {
				_ = proc.Signal(syscall.SIGTERM)
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
		InstallState: installState,
		InstalledAt:  state.InstalledAt,
	}
	return m.saveState(name, preserved)
}

// cleanupStaleVM clears persisted running state for a VM whose process died on
// its own (e.g. guest OS shutdown). It does NOT touch /etc/hosts — callers
// that want hostname unregistration must call UnregisterHostname themselves.
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
	return os.RemoveAll(m.vmDir(name))
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
	cpuFlags := cfg.CPUFlags
	if cfg.GPU.Mode == GPUPassthrough && cfg.GPU.AntiDetect {
		cpuFlags = append(cpuFlags, qemu.GamingCPUFlags...)
	}
	sockets, cores, threads := cputil.AdjustSMP(cfg.CPUs, cfg.Sockets, cfg.Cores, cfg.Threads)
	cpu := qemu.NewCPU(m.provider,
		qemu.WithCPUModel(qemu.CPUModel(cfg.CPUModel)),
		qemu.WithSMP(cfg.CPUs, sockets, threads, cores),
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
	if cfg.SSHPort > 0 {
		port := availablePort(cfg.SSHPort, 2200, 2299)
		cfg.SSHPort = port
		nicHostFwds = append(nicHostFwds, fmt.Sprintf("tcp:127.0.0.1:%d-:22", port))
	}
	nic := qemu.NewNIC(qemu.NICMode(cfg.NIC.Mode), cfg.NIC.Bridge, cfg.NIC.MAC, nicHostFwds...)
	if cfg.NIC.Mode == "bridge" && cfg.CPUs > 1 {
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
			for _, addr := range allAddrs {
				ds, resetErr := gpu.EnsureReady(addr)
				if resetErr != nil {
					m.provider.Logger().Warn("VFIO device D3cold reset failed — cold reboot required",
						zap.String("pci_addr", addr),
						zap.String("power_state", string(ds.PowerState)),
						zap.String("runtime_status", ds.RuntimeStatus),
						zap.Error(resetErr),
					)
				} else if ds.NeedsAttention() {
					m.provider.Logger().Warn("VFIO device in D3hot/suspended — vfio-pci will attempt runtime resume",
						zap.String("pci_addr", addr),
						zap.String("power_state", string(ds.PowerState)),
						zap.String("runtime_status", ds.RuntimeStatus),
					)
				} else {
					m.provider.Logger().Info("VFIO device ready",
						zap.String("pci_addr", addr),
						zap.String("power_state", string(ds.PowerState)),
						zap.String("runtime_status", ds.RuntimeStatus),
					)
				}
			}
			primary := qemu.NewVFIODevice(cfg.GPU.PCIAddr)
			if cfg.GPU.ROMFile != "" {
				primary.ROMFile = cfg.GPU.ROMFile
			}
			opts = append(opts, qemu.WithVFIO(primary))
			// Peer devices (e.g. GPU HDMI/DP audio) must be in the same VFIO
			// container; each gets its own PCIe root port (pcie.2, pcie.3, …).
			for i, addr := range cfg.GPU.ExtraVFIOAddrs {
				busID := fmt.Sprintf("pcie.%d", i+2)
				slot := i + 2
				peer := qemu.NewVFIOPeerDevice(addr, busID, slot)
				opts = append(opts, qemu.WithVFIO(peer))
				m.provider.Logger().Info("attaching VFIO peer device",
					zap.String("pci_addr", addr),
					zap.String("bus_id", busID),
				)
			}
		} else {
			m.provider.Logger().Warn("GPU mode is passthrough but no pci_addr configured — no VFIO device will be attached")
		}
		// Passthrough VMs need shared memory for the vhost-user protocol.
		opts = append(opts, qemu.WithMemfd(qemu.NewMemfdBackend(cfg.Memory)))
	case GPUVirtio:
		opts = append(opts, qemu.WithVGA("none"))
		opts = append(opts, qemu.WithDevice("virtio-vga-gl"))
		opts = append(opts, qemu.WithDisplay("gtk,gl=on"))
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

	// Virtiofsd mounts — ensure the binary exists before starting any daemon.
	var virtiofsdPIDs []int
	if len(cfg.VirtiofsMounts) > 0 {
		home, homeErr := os.UserHomeDir()
		if homeErr == nil {
			if path, ensureErr := virtiofsdinstall.EnsureVirtiofsd(home); ensureErr == nil {
				m.provider.Config().VirtiofsdPath = path
			} else {
				m.provider.Logger().Warn("virtiofsd not available", zap.Error(ensureErr))
			}
		}
	}
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

	// TPM — start swtpm daemon before QEMU.
	if cfg.TPM != nil && cfg.TPM.Enabled {
		tpmDir := filepath.Join(m.vmDir(cfg.Name), "tpm")
		tpmSock := filepath.Join(m.vmDir(cfg.Name), "tpm.sock")
		if err := startSwtpm(tpmDir, tpmSock); err != nil {
			return nil, nil, fmt.Errorf("swtpm: %w", err)
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

	// CPU pinning — applied post-launch via taskset on vCPU threads.
	if len(cfg.CPUPinning) > 0 {
		opts = append(opts, qemu.WithCPUPinning(cfg.CPUPinning))
	}

	// RTC
	if cfg.RTC != "" {
		opts = append(opts, qemu.WithRTC(cfg.RTC))
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
