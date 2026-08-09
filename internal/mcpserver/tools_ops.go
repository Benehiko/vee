package mcpserver

// Operational tools beyond the core VM lifecycle: health checks, guest
// ports, service discovery, QMP, stats, display info, config, autostart,
// move, backups, image cache, GPU inspection, runner credentials, and the
// pacman mirror. Together with server.go this covers every non-interactive
// vee CLI command; the mapping is declared in coverage.go and enforced by
// cmd's TestMCPCoversCLI.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/backup"
	"github.com/Benehiko/vee/internal/gpu"
	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/mirror"
	"github.com/Benehiko/vee/internal/monitor"
	"github.com/Benehiko/vee/internal/runnercreds"
	"github.com/Benehiko/vee/internal/runnerssh"
	"github.com/Benehiko/vee/internal/vm"
)

func (s *server) registerOps(srv *mcp.Server) []string {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	var names []string

	addTool(srv, &names, &mcp.Tool{
		Name:        "vm_check",
		Description: "Run the VM's health checks now (the guest's vee-check script) and return the results. Requires a running, installed VM.",
	}, s.vmCheck)

	addTool(srv, &names, &mcp.Tool{
		Name: "vm_network",
		Description: "Report a running VM's network state: interfaces, default route, DNS, firewall (ufw), VPN/kill-switch status, and egress IP, " +
			"with template-aware pass/fail checks for VPN-configured VMs. Set skip_egress=true to avoid the external egress-IP lookups.",
		Annotations: readOnly,
	}, s.vmNetwork)

	addTool(srv, &names, &mcp.Tool{
		Name:        "vm_ports",
		Description: "List listening TCP ports and their process names inside a running guest (via the QEMU guest agent).",
		Annotations: readOnly,
	}, s.vmPorts)

	addTool(srv, &names, &mcp.Tool{
		Name:        "vm_services",
		Description: "List the services a VM declares (HTTP/HTTPS/SPICE/TCP) with how to reach each one from the host.",
		Annotations: readOnly,
	}, s.vmServices)

	addTool(srv, &names, &mcp.Tool{
		Name: "vm_qmp",
		Description: "Send a raw QMP (QEMU Machine Protocol) command to a running QEMU-backed VM and return the response. " +
			"Example: command=query-status. Routed via the vee daemon when it holds the VM's QMP socket.",
	}, s.vmQMP)

	addTool(srv, &names, &mcp.Tool{
		Name: "vm_stats",
		Description: "Sample a running QEMU-backed VM's CPU, memory, disk, and network counters once (takes about a second). " +
			"May be unavailable while another process (the daemon or a dashboard) holds the VM's QMP socket.",
		Annotations: readOnly,
	}, s.vmStats)

	addTool(srv, &names, &mcp.Tool{
		Name:        "vm_display",
		Description: "Describe how to connect to a VM's display: VNC for macOS guests, SPICE URL, or Moonlight instructions for GPU passthrough.",
		Annotations: readOnly,
	}, s.vmDisplay)

	addTool(srv, &names, &mcp.Tool{
		Name:        "vm_config_get",
		Description: "Return a VM's full persisted configuration (vm.yaml) as JSON.",
		Annotations: readOnly,
	}, s.vmConfigGet)

	addTool(srv, &names, &mcp.Tool{
		Name:        "vm_autostart",
		Description: "Get or set whether the vee daemon boots this VM automatically. Omit enabled to read the current value.",
	}, s.vmAutostart)

	addTool(srv, &names, &mcp.Tool{
		Name: "vm_move",
		Description: "Move a VM's managed boot disk to another host directory (e.g. a faster NVMe). " +
			"A running VM is stopped first and restarted afterwards unless restart=false.",
	}, s.vmMove)

	addTool(srv, &names, &mcp.Tool{
		Name:        "vm_backup_list",
		Description: "List past and in-progress backup runs for a VM.",
		Annotations: readOnly,
	}, s.vmBackupList)

	addTool(srv, &names, &mcp.Tool{
		Name: "vm_backup",
		Description: "Back up directories from a running VM to the host via rsync over SSH. dirs are absolute guest paths. " +
			"Long-running for large directories.",
	}, s.vmBackup)

	addTool(srv, &names, &mcp.Tool{
		Name:        "image_catalog",
		Description: "List the base-image distros vee can pull, with their known versions (newest first).",
		Annotations: readOnly,
	}, s.imageCatalog)

	addTool(srv, &names, &mcp.Tool{
		Name: "image_pull",
		Description: "Download (or build) a base image into the local cache so later creates reuse it. A no-op if already cached. " +
			"Can take many minutes for large images (macOS IPSW ~14 GB; Windows builds an ISO).",
	}, s.imagePull)

	addTool(srv, &names, &mcp.Tool{
		Name:        "gpu_list",
		Description: "List PCI devices grouped by IOMMU group, marking GPUs and their bound drivers (Linux hosts).",
		Annotations: readOnly,
	}, s.gpuList)

	addTool(srv, &names, &mcp.Tool{
		Name:        "gpu_status",
		Description: "Pre-flight check a PCI device for VFIO passthrough: driver binding, IOMMU group peers, /dev/vfio access, memlock limits.",
		Annotations: readOnly,
	}, s.gpuStatus)

	addTool(srv, &names, &mcp.Tool{
		Name: "runner_key",
		Description: "Return a GitHub Actions runner's SSH public key. Without name, returns (creating if needed) the shared global runner key; " +
			"with name, returns that runner's per-instance key.",
	}, s.runnerKey)

	addTool(srv, &names, &mcp.Tool{
		Name:        "runner_snapshot",
		Description: "Persist a running github-runner VM's registration credentials to the host (encrypted), so a later reinstall rejoins GitHub without a new token.",
	}, s.runnerSnapshot)

	addTool(srv, &names, &mcp.Tool{
		Name:        "mirror_status",
		Description: "Show the host-side pacman caching proxy's state: installed, active, and cache size (Linux hosts).",
		Annotations: readOnly,
	}, s.mirrorStatus)

	addTool(srv, &names, &mcp.Tool{
		Name:        "mirror_start",
		Description: "Install (if needed) and start the host-side pacman caching proxy (pacoloco) as a systemd user unit.",
	}, s.mirrorStart)

	addTool(srv, &names, &mcp.Tool{
		Name:        "mirror_stop",
		Description: "Stop the host-side pacman caching proxy.",
	}, s.mirrorStop)

	addTool(srv, &names, &mcp.Tool{
		Name:        "mirror_purge",
		Description: "Delete all cached pacman packages on disk.",
	}, s.mirrorPurge)

	return names
}

// ---- vm_check ----

type vmCheckOut struct {
	Checks []vm.HealthCheck `json:"checks"`
}

func (s *server) vmCheck(ctx context.Context, _ *mcp.CallToolRequest, in vmNameIn) (*mcp.CallToolResult, vmCheckOut, error) {
	checks, err := s.mgr.RunHealthCheck(ctx, in.Name)
	if err != nil {
		return nil, vmCheckOut{}, err
	}
	return nil, vmCheckOut{Checks: checks}, nil
}

// ---- vm_network ----

type vmNetworkIn struct {
	Name       string `json:"name"`
	SkipEgress bool   `json:"skip_egress,omitempty" jsonschema:"skip the external egress-IP lookups (no HTTPS/DNS requests leave the host or guest)"`
}

type vmNetworkOut struct {
	Report *vm.NetworkReport `json:"report"`
}

func (s *server) vmNetwork(ctx context.Context, _ *mcp.CallToolRequest, in vmNetworkIn) (*mcp.CallToolResult, vmNetworkOut, error) {
	report, err := s.mgr.QueryNetwork(ctx, in.Name, vm.NetworkOptions{SkipEgress: in.SkipEgress})
	if err != nil {
		return nil, vmNetworkOut{}, err
	}
	return nil, vmNetworkOut{Report: report}, nil
}

// ---- vm_ports ----

type vmPortsOut struct {
	Ports []vm.GuestPort `json:"ports"`
}

func (s *server) vmPorts(ctx context.Context, _ *mcp.CallToolRequest, in vmNameIn) (*mcp.CallToolResult, vmPortsOut, error) {
	_, state, err := s.loadRunningVM(in.Name)
	if err != nil {
		return nil, vmPortsOut{}, err
	}
	if state.QGASocket == "" {
		return nil, vmPortsOut{}, fmt.Errorf("VM %q was not started with guest agent support; recreate with a template that enables guest_agent", in.Name)
	}
	ports, err := vm.QueryGuestPorts(ctx, state.QGASocket, 5*time.Second)
	if err != nil {
		return nil, vmPortsOut{}, err
	}
	return nil, vmPortsOut{Ports: ports}, nil
}

// ---- vm_services ----

type serviceOut struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Port     int    `json:"port" jsonschema:"guest-side port (or host-side for SPICE)"`
	URL      string `json:"url" jsonschema:"how to reach it from the host; a <proxy> placeholder means a tunnel is needed (vee tunnel)"`
}

type vmServicesOut struct {
	Services []serviceOut `json:"services"`
}

func (s *server) vmServices(_ context.Context, _ *mcp.CallToolRequest, in vmNameIn) (*mcp.CallToolResult, vmServicesOut, error) {
	cfg, state, err := s.loadVM(in.Name)
	if err != nil {
		return nil, vmServicesOut{}, err
	}
	out := vmServicesOut{Services: []serviceOut{}}
	for _, rs := range vm.ResolvedServices(cfg, state) {
		out.Services = append(out.Services, serviceOut{
			Name:     rs.Name,
			Protocol: string(rs.Protocol),
			Port:     rs.Port,
			URL:      vm.ServiceURL(cfg, rs),
		})
	}
	return nil, vmServicesOut{Services: out.Services}, nil
}

// ---- vm_qmp ----

type vmQMPIn struct {
	Name    string         `json:"name" jsonschema:"name of the VM"`
	Command string         `json:"command" jsonschema:"QMP execute name, e.g. query-status or system_powerdown"`
	Args    map[string]any `json:"args,omitempty" jsonschema:"QMP command arguments"`
}

type vmQMPOut struct {
	Result any `json:"result"`
}

func (s *server) vmQMP(ctx context.Context, _ *mcp.CallToolRequest, in vmQMPIn) (*mcp.CallToolResult, vmQMPOut, error) {
	_, _, err := s.loadRunningVM(in.Name)
	if err != nil {
		return nil, vmQMPOut{}, err
	}
	raw, err := s.mgr.QMPExecute(ctx, in.Name, in.Command, in.Args, 5*time.Second)
	if err != nil {
		return nil, vmQMPOut{}, err
	}
	var result any
	if err := json.Unmarshal(raw, &result); err != nil {
		result = string(raw)
	}
	return nil, vmQMPOut{Result: result}, nil
}

// ---- vm_stats ----

type vmStatsOut struct {
	CPUPercent      float64 `json:"cpu_percent"`
	MemActualBytes  uint64  `json:"mem_actual_bytes"`
	DiskReadBytesS  uint64  `json:"disk_read_bytes_per_s"`
	DiskWriteBytesS uint64  `json:"disk_write_bytes_per_s"`
	NetRxBytesS     uint64  `json:"net_rx_bytes_per_s"`
	NetTxBytesS     uint64  `json:"net_tx_bytes_per_s"`
}

func (s *server) vmStats(ctx context.Context, _ *mcp.CallToolRequest, in vmNameIn) (*mcp.CallToolResult, vmStatsOut, error) {
	_, state, err := s.loadRunningVM(in.Name)
	if err != nil {
		return nil, vmStatsOut{}, err
	}
	if state.QMPSocket == "" {
		return nil, vmStatsOut{}, fmt.Errorf("VM %q has no QMP socket (stats need the QEMU backend)", in.Name)
	}

	// The poller swallows its first tick to establish a delta baseline, so
	// one sample lands after ~2 intervals.
	poller, err := monitor.NewPoller(ctx, state.QMPSocket, 500*time.Millisecond)
	if err != nil {
		return nil, vmStatsOut{}, fmt.Errorf("dial QMP socket (another process, e.g. the vee daemon, may hold it): %w", err)
	}
	defer poller.Close()

	select {
	case stats := <-poller.Ch:
		return nil, vmStatsOut{
			CPUPercent:      stats.CPUPercent,
			MemActualBytes:  stats.MemActual,
			DiskReadBytesS:  stats.DiskReadBytes,
			DiskWriteBytesS: stats.DiskWriteBytes,
			NetRxBytesS:     stats.NetRxBytes,
			NetTxBytesS:     stats.NetTxBytes,
		}, nil
	case <-time.After(10 * time.Second):
		return nil, vmStatsOut{}, fmt.Errorf("timed out sampling stats for %q", in.Name)
	case <-ctx.Done():
		return nil, vmStatsOut{}, ctx.Err()
	}
}

// ---- vm_display ----

type vmDisplayOut struct {
	Kind      string `json:"kind" jsonschema:"vnc, spice, moonlight, or window"`
	URL       string `json:"url,omitempty"`
	Reachable *bool  `json:"reachable,omitempty" jsonschema:"whether the display endpoint answered a TCP probe"`
	Hint      string `json:"hint,omitempty"`
}

func (s *server) vmDisplay(ctx context.Context, _ *mcp.CallToolRequest, in vmNameIn) (*mcp.CallToolResult, vmDisplayOut, error) {
	cfg, state, err := s.loadRunningVM(in.Name)
	if err != nil {
		return nil, vmDisplayOut{}, err
	}

	// macOS guests expose macOS Screen Sharing (VNC) on the guest IP.
	if state.BackendName() == backend.VZ {
		if cfg.NIC.MAC == "" {
			return nil, vmDisplayOut{}, fmt.Errorf("VM %q has no MAC on record; cannot resolve its IP", in.Name)
		}
		ip, err := vm.ResolveIPFromMAC(cfg.NIC.MAC)
		if err != nil {
			return nil, vmDisplayOut{}, fmt.Errorf("resolve guest IP: %w", err)
		}
		reachable := probeTCP(ctx, net.JoinHostPort(ip, "5900"))
		return nil, vmDisplayOut{
			Kind:      "vnc",
			URL:       "vnc://" + ip,
			Reachable: &reachable,
			Hint:      "open with a VNC/Screen Sharing client",
		}, nil
	}

	// GPU passthrough guests stream via Sunshine/Moonlight, not SPICE.
	if cfg.GPU.Mode == vm.GPUPassthrough {
		return nil, vmDisplayOut{
			Kind: "moonlight",
			URL:  fmt.Sprintf("%s:47989", hostLANIP(ctx)),
			Hint: "pair a Moonlight client with the Sunshine server running in the guest",
		}, nil
	}

	port := state.SPICEPort
	if port == 0 && cfg.SPICE != nil {
		port = cfg.SPICE.Port
	}
	if port > 0 {
		url := fmt.Sprintf("spice://localhost:%d", port)
		reachable := probeTCP(ctx, fmt.Sprintf("localhost:%d", port))
		return nil, vmDisplayOut{
			Kind:      "spice",
			URL:       url,
			Reachable: &reachable,
			Hint:      "open with remote-viewer, spicy, or remmina",
		}, nil
	}

	if cfg.GPU.Mode == vm.GPUVirtio {
		return nil, vmDisplayOut{
			Kind: "window",
			Hint: "virtio-gpu guests open a graphical window on the host when started",
		}, nil
	}
	return nil, vmDisplayOut{}, fmt.Errorf("VM %q has no display configured", in.Name)
}

func probeTCP(ctx context.Context, addr string) bool {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// hostLANIP returns the host's outbound LAN IP (best-effort).
func hostLANIP(ctx context.Context) string {
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", "8.8.8.8:80")
	if err != nil {
		return "<host-ip>"
	}
	defer func() { _ = conn.Close() }()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return "<host-ip>"
}

// ---- vm_config_get ----

type vmConfigOut struct {
	Config *vm.VMConfig `json:"config"`
}

func (s *server) vmConfigGet(_ context.Context, _ *mcp.CallToolRequest, in vmNameIn) (*mcp.CallToolResult, vmConfigOut, error) {
	cfg, err := s.mgr.LoadConfig(in.Name)
	if err != nil {
		return nil, vmConfigOut{}, fmt.Errorf("VM %q not found: %w", in.Name, err)
	}
	return nil, vmConfigOut{Config: cfg}, nil
}

// ---- vm_autostart ----

type vmAutostartIn struct {
	Name    string `json:"name" jsonschema:"name of the VM"`
	Enabled *bool  `json:"enabled,omitempty" jsonschema:"set autostart on/off; omit to read the current value"`
}

type vmAutostartOut struct {
	Name      string `json:"name"`
	Autostart bool   `json:"autostart"`
}

func (s *server) vmAutostart(_ context.Context, _ *mcp.CallToolRequest, in vmAutostartIn) (*mcp.CallToolResult, vmAutostartOut, error) {
	if in.Enabled != nil {
		if err := s.mgr.SetAutoStart(in.Name, *in.Enabled); err != nil {
			return nil, vmAutostartOut{}, err
		}
	}
	cfg, err := s.mgr.LoadConfig(in.Name)
	if err != nil {
		return nil, vmAutostartOut{}, fmt.Errorf("VM %q not found: %w", in.Name, err)
	}
	return nil, vmAutostartOut{Name: in.Name, Autostart: cfg.AutoStart}, nil
}

// ---- vm_move ----

type vmMoveIn struct {
	Name      string `json:"name" jsonschema:"name of the VM"`
	TargetDir string `json:"target_dir" jsonschema:"host directory to move the boot disk into"`
	Restart   *bool  `json:"restart,omitempty" jsonschema:"restart the VM afterwards if it was running (default true)"`
}

type vmMoveOut struct {
	Name      string `json:"name"`
	OldPath   string `json:"old_path"`
	NewPath   string `json:"new_path"`
	Restarted bool   `json:"restarted"`
}

func (s *server) vmMove(ctx context.Context, _ *mcp.CallToolRequest, in vmMoveIn) (*mcp.CallToolResult, vmMoveOut, error) {
	wasRunning := false
	if state, err := s.mgr.LoadState(in.Name); err == nil && state.Running {
		wasRunning = true
		if err := s.mgr.Stop(ctx, in.Name); err != nil {
			return nil, vmMoveOut{}, fmt.Errorf("stop VM before move: %w", err)
		}
	}
	oldPath, newPath, err := s.mgr.MoveBootDisk(in.Name, in.TargetDir)
	if err != nil {
		return nil, vmMoveOut{}, err
	}
	out := vmMoveOut{Name: in.Name, OldPath: oldPath, NewPath: newPath}
	if wasRunning && (in.Restart == nil || *in.Restart) {
		if err := s.startDetached(ctx, in.Name); err != nil {
			return nil, out, fmt.Errorf("disk moved, but restart failed: %w", err)
		}
		out.Restarted = true
	}
	return nil, out, nil
}

// ---- vm_backup_list ----

type backupRunOut struct {
	ID         int64    `json:"id"`
	Dest       string   `json:"dest"`
	Status     string   `json:"status"`
	StartedAt  string   `json:"started_at,omitempty"`
	FinishedAt string   `json:"finished_at,omitempty"`
	Error      string   `json:"error,omitempty"`
	Dirs       []string `json:"dirs"`
}

type vmBackupListOut struct {
	Runs []backupRunOut `json:"runs"`
}

func (s *server) vmBackupList(_ context.Context, _ *mcp.CallToolRequest, in vmNameIn) (*mcp.CallToolResult, vmBackupListOut, error) {
	runs, err := backup.ListRuns(s.prov.DB(), in.Name) //nolint:contextcheck // backup's DB API takes no context; bounded local sqlite reads
	if err != nil {
		return nil, vmBackupListOut{}, err
	}
	out := vmBackupListOut{Runs: []backupRunOut{}}
	for _, r := range runs {
		ro := backupRunOut{ID: r.ID, Dest: r.Dest, Status: string(r.Status), Error: r.Error, Dirs: r.Dirs}
		if r.StartedAt != nil {
			ro.StartedAt = r.StartedAt.Format(time.RFC3339)
		}
		if r.FinishedAt != nil {
			ro.FinishedAt = r.FinishedAt.Format(time.RFC3339)
		}
		out.Runs = append(out.Runs, ro)
	}
	return nil, out, nil
}

// ---- vm_backup ----

type vmBackupIn struct {
	Name string   `json:"name" jsonschema:"name of the VM"`
	Dirs []string `json:"dirs" jsonschema:"absolute guest directories to back up"`
	Dest string   `json:"dest,omitempty" jsonschema:"host destination directory; default ~/.vee/vms/<name>/backups/<date>"`
}

type vmBackupOut struct {
	RunID  int64    `json:"run_id"`
	Dest   string   `json:"dest"`
	Status string   `json:"status"`
	Dirs   []string `json:"dirs"`
}

func (s *server) vmBackup(_ context.Context, _ *mcp.CallToolRequest, in vmBackupIn) (*mcp.CallToolResult, vmBackupOut, error) {
	if len(in.Dirs) == 0 {
		return nil, vmBackupOut{}, fmt.Errorf("dirs is required: absolute guest paths to back up (discover candidates with vm_exec, e.g. ls ~)")
	}
	cfg, state, err := s.loadRunningVM(in.Name)
	if err != nil {
		return nil, vmBackupOut{}, err
	}
	conn, err := backupSSHConn(cfg, state)
	if err != nil {
		return nil, vmBackupOut{}, err
	}

	dest := in.Dest
	if dest == "" {
		dest = filepath.Join(s.prov.Config().StoragePath, in.Name, "backups", time.Now().Format("2006-01-02"))
	}
	if err := os.MkdirAll(dest, 0o750); err != nil {
		return nil, vmBackupOut{}, err
	}

	db := s.prov.DB()
	runID, err := backup.CreateRun(db, in.Name, dest, in.Dirs) //nolint:contextcheck // backup's DB API takes no context; bounded local sqlite writes
	if err != nil {
		return nil, vmBackupOut{}, err
	}
	runner := &backup.Runner{DB: db, Conn: conn}
	if err := runner.Execute(runID, dest, in.Dirs); err != nil { //nolint:contextcheck // backup.Runner.Execute takes no context; same contract as the CLI
		return nil, vmBackupOut{RunID: runID, Dest: dest, Status: string(backup.StatusFailed), Dirs: in.Dirs}, err
	}
	return nil, vmBackupOut{RunID: runID, Dest: dest, Status: string(backup.StatusDone), Dirs: in.Dirs}, nil
}

// backupSSHConn resolves the SSH connection for a backup like the CLI does,
// minus the interactive fallback: persisted ssh_host, forwarded SSH port, or
// MAC-based IP resolution — in that order.
func backupSSHConn(cfg *vm.VMConfig, state *vm.VMState) (backup.SSHConn, error) {
	user := cfg.SSHUser
	if user == "" && cfg.CloudInit != nil && cfg.CloudInit.User != "" {
		user = cfg.CloudInit.User
	}
	identity := veeIdentity()

	switch {
	case cfg.SSHHost != "":
		host, port, err := splitSSHHost(cfg.SSHHost)
		if err != nil {
			return backup.SSHConn{}, fmt.Errorf("parse ssh_host %q: %w", cfg.SSHHost, err)
		}
		return backup.SSHConn{User: user, Host: host, Port: port, Identity: identity}, nil
	case state.SSHPort > 0:
		return backup.SSHConn{User: user, Host: "127.0.0.1", Port: state.SSHPort, Identity: identity}, nil
	case cfg.NIC.MAC != "":
		ip, err := vm.ResolveIPFromMAC(cfg.NIC.MAC)
		if err != nil {
			return backup.SSHConn{}, fmt.Errorf("cannot resolve the VM's SSH endpoint automatically (%w); set ssh_host in its config or run `vee backup` once interactively", err)
		}
		return backup.SSHConn{User: user, Host: ip, Port: 22, Identity: identity}, nil
	default:
		return backup.SSHConn{}, fmt.Errorf("cannot resolve the VM's SSH endpoint automatically; set ssh_host in its config or run `vee backup` once interactively")
	}
}

// splitSSHHost splits "host", "host:port", or "[host]:port"; port defaults to 22.
func splitSSHHost(s string) (string, int, error) {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		return s, 22, nil //nolint:nilerr // missing port is not an error; default to 22
	}
	var port int
	if _, err := fmt.Sscan(p, &port); err != nil || port <= 0 {
		return "", 0, fmt.Errorf("invalid port %q", p)
	}
	return h, port, nil
}

// veeIdentity returns ~/.vee/ssh/id_ed25519 if it exists, else "".
func veeIdentity() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, ".vee", "ssh", "id_ed25519")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// ---- image_catalog / image_pull ----

type imageDistroOut struct {
	Distro   string   `json:"distro"`
	Versions []string `json:"versions" jsonschema:"known versions, newest first"`
}

type imageCatalogOut struct {
	Distros []imageDistroOut `json:"distros"`
}

func (s *server) imageCatalog(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, imageCatalogOut, error) {
	out := imageCatalogOut{Distros: []imageDistroOut{}}
	for _, d := range images.PullableDistros() {
		out.Distros = append(out.Distros, imageDistroOut{Distro: d, Versions: images.DistroVersions(d)})
	}
	return nil, out, nil
}

type imagePullIn struct {
	Distro  string `json:"distro" jsonschema:"distro to pull; see image_catalog"`
	Version string `json:"version,omitempty" jsonschema:"version to pull; default latest"`
}

type imagePullOut struct {
	Distro  string `json:"distro"`
	Version string `json:"version"`
	Path    string `json:"path"`
}

func (s *server) imagePull(ctx context.Context, _ *mcp.CallToolRequest, in imagePullIn) (*mcp.CallToolResult, imagePullOut, error) {
	version := in.Version
	if version == "" {
		version = "latest"
	}
	img, err := images.NewImage(s.prov, in.Distro, version)
	if err != nil {
		return nil, imagePullOut{}, err
	}
	if err := img.Download(ctx); err != nil {
		return nil, imagePullOut{}, err
	}
	return nil, imagePullOut{Distro: img.Distro(), Version: img.Version(), Path: img.AbsolutePath()}, nil
}

// ---- gpu_list / gpu_status ----

type gpuDeviceOut struct {
	IOMMUGroup int    `json:"iommu_group"`
	Address    string `json:"address"`
	Name       string `json:"name,omitempty"`
	Vendor     string `json:"vendor"`
	Device     string `json:"device"`
	Class      string `json:"class"`
	Driver     string `json:"driver,omitempty"`
	IsGPU      bool   `json:"is_gpu"`
}

type gpuListOut struct {
	Devices []gpuDeviceOut `json:"devices"`
}

func (s *server) gpuList(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, gpuListOut, error) {
	groups, err := gpu.ListIOMMUGroups()
	if err != nil {
		return nil, gpuListOut{}, err
	}
	out := gpuListOut{Devices: []gpuDeviceOut{}}
	for _, g := range groups {
		for _, d := range g.Devices {
			out.Devices = append(out.Devices, gpuDeviceOut{
				IOMMUGroup: g.ID,
				Address:    d.Address,
				Name:       gpu.LookupDeviceName(d.Vendor, d.Device),
				Vendor:     d.Vendor,
				Device:     d.Device,
				Class:      d.Class,
				Driver:     d.Driver,
				IsGPU:      d.IsGPU,
			})
		}
	}
	return nil, out, nil
}

type gpuStatusIn struct {
	PCI    string `json:"pci" jsonschema:"PCI address, e.g. 0000:0b:00.0"`
	Memory string `json:"memory,omitempty" jsonschema:"planned guest memory (e.g. 16G) for the memlock check"`
}

type gpuStatusOut struct {
	PCI             string            `json:"pci"`
	Driver          string            `json:"driver,omitempty"`
	IOMMUGroup      int               `json:"iommu_group"`
	GroupPeers      []string          `json:"group_peers,omitempty"`
	VFIODevPath     string            `json:"vfio_dev_path,omitempty"`
	VFIOAccessible  bool              `json:"vfio_accessible"`
	MemlockSoft     string            `json:"memlock_soft,omitempty"`
	MemlockRequired string            `json:"memlock_required,omitempty"`
	MemlockOK       bool              `json:"memlock_ok"`
	PowerState      string            `json:"power_state,omitempty"`
	RuntimeStatus   string            `json:"runtime_status,omitempty"`
	OK              bool              `json:"ok"`
	Problems        map[string]string `json:"problems,omitempty"`
}

func (s *server) gpuStatus(_ context.Context, _ *mcp.CallToolRequest, in gpuStatusIn) (*mcp.CallToolResult, gpuStatusOut, error) {
	res := gpu.PreflightCheck(in.PCI, in.Memory)
	out := gpuStatusOut{
		PCI:             res.PCIAddr,
		Driver:          res.Driver,
		IOMMUGroup:      res.IOMMUGroup,
		VFIODevPath:     res.VFIODevPath,
		VFIOAccessible:  res.VFIOAccessible,
		MemlockSoft:     gpu.FormatBytes(res.MemlockSoftBytes),
		MemlockRequired: gpu.FormatBytes(res.MemlockRequiredBytes),
		MemlockOK:       res.MemlockOK(),
		PowerState:      string(res.DeviceState.PowerState),
		RuntimeStatus:   res.DeviceState.RuntimeStatus,
		OK:              res.OK(),
	}
	for _, p := range res.GroupPeers {
		out.GroupPeers = append(out.GroupPeers, p.Address)
	}
	if len(res.Errors) > 0 {
		out.Problems = make(map[string]string, len(res.Errors))
		for k, e := range res.Errors {
			out.Problems[k] = e.Error()
		}
	}
	return nil, out, nil
}

// ---- runner_key / runner_snapshot ----

type runnerKeyIn struct {
	Name string `json:"name,omitempty" jsonschema:"VM name for a per-instance key; omit for the shared global runner key"`
}

type runnerKeyOut struct {
	PublicKey string `json:"public_key"`
	Created   bool   `json:"created" jsonschema:"true when the global key was newly generated by this call"`
}

func (s *server) runnerKey(_ context.Context, _ *mcp.CallToolRequest, in runnerKeyIn) (*mcp.CallToolResult, runnerKeyOut, error) {
	if in.Name == "" {
		id, err := runnercreds.LoadOrCreateIdentity()
		if err != nil {
			return nil, runnerKeyOut{}, fmt.Errorf("load age identity: %w", err)
		}
		pub, created, err := runnerssh.EnsureKey(id, "")
		if err != nil {
			return nil, runnerKeyOut{}, err
		}
		return nil, runnerKeyOut{PublicKey: pub, Created: created}, nil
	}
	pub, ok, err := runnerssh.PublicKey(in.Name)
	if err != nil {
		return nil, runnerKeyOut{}, err
	}
	if !ok {
		return nil, runnerKeyOut{}, fmt.Errorf("no per-instance key for %q; create the runner with runner_ssh_key=true", in.Name)
	}
	return nil, runnerKeyOut{PublicKey: pub}, nil
}

type runnerSnapshotOut struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (s *server) runnerSnapshot(ctx context.Context, _ *mcp.CallToolRequest, in vmNameIn) (*mcp.CallToolResult, runnerSnapshotOut, error) {
	cfg, state, err := s.loadRunningVM(in.Name)
	if err != nil {
		return nil, runnerSnapshotOut{}, err
	}
	if cfg.Template != "github-runner" {
		return nil, runnerSnapshotOut{}, fmt.Errorf("VM %q is a %q, not a github-runner", in.Name, cfg.Template)
	}
	if state.SSHPort == 0 {
		return nil, runnerSnapshotOut{}, fmt.Errorf("VM %q has no forwarded SSH port", in.Name)
	}
	user := cfg.SSHUser
	if user == "" && cfg.CloudInit != nil {
		user = cfg.CloudInit.User
	}

	id, err := runnercreds.LoadOrCreateIdentity()
	if err != nil {
		return nil, runnerSnapshotOut{}, fmt.Errorf("load age identity: %w", err)
	}
	ssh := runnercreds.NewSSHRunner(user, "127.0.0.1", state.SSHPort, veeIdentity())
	snapCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := runnercreds.Snapshot(snapCtx, ssh, id, in.Name); err != nil {
		return nil, runnerSnapshotOut{}, err
	}
	path, err := runnercreds.SnapshotPath(in.Name)
	if err != nil {
		return nil, runnerSnapshotOut{}, err
	}
	return nil, runnerSnapshotOut{Name: in.Name, Path: path}, nil
}

// ---- mirror ----

type mirrorStatusOut struct {
	Installed      bool   `json:"installed"`
	Active         bool   `json:"active"`
	CacheSizeBytes int64  `json:"cache_size_bytes"`
	CacheDir       string `json:"cache_dir,omitempty"`
	Port           int    `json:"port"`
}

func (s *server) mirrorStatus(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, mirrorStatusOut, error) {
	st, err := mirror.GetStatus(ctx)
	if err != nil {
		return nil, mirrorStatusOut{}, err
	}
	out := mirrorStatusOut{
		Installed:      st.Installed,
		Active:         st.Active,
		CacheSizeBytes: st.CacheSize,
		Port:           mirror.DefaultPort,
	}
	if st.Paths != nil {
		out.CacheDir = st.Paths.CacheDir
	}
	return nil, out, nil
}

type mirrorActionOut struct {
	Status string `json:"status"`
}

func (s *server) mirrorStart(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, mirrorActionOut, error) {
	if err := mirror.Start(ctx); err != nil {
		return nil, mirrorActionOut{}, err
	}
	return nil, mirrorActionOut{Status: "started"}, nil
}

func (s *server) mirrorStop(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, mirrorActionOut, error) {
	if err := mirror.Stop(ctx); err != nil {
		return nil, mirrorActionOut{}, err
	}
	return nil, mirrorActionOut{Status: "stopped"}, nil
}

func (s *server) mirrorPurge(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, mirrorActionOut, error) {
	if err := mirror.Purge(); err != nil {
		return nil, mirrorActionOut{}, err
	}
	return nil, mirrorActionOut{Status: "purged"}, nil
}
