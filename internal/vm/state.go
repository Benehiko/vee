package vm

import (
	"time"

	"github.com/Benehiko/vee/internal/backend"
)

const (
	InstallStatePending = "pending"
	InstallStateReady   = "ready"
)

// Desired states for the daemon's autostart loop.
const (
	DesiredStateRunning = "running"
	DesiredStateStopped = "stopped"
)

// Reasons recorded in LastShutdownReason so the daemon can distinguish
// user intent ("don't restart") from crashes ("do restart").
const (
	ShutdownReasonUser  = "user"  // vee stop / vee stop --force
	ShutdownReasonGuest = "guest" // guest OS initiated poweroff (e.g. `poweroff` inside the VM)
	ShutdownReasonCrash = "crash" // QEMU exited without a recorded reason
	ShutdownReasonHost  = "host"  // daemon stopped the VM because the host is shutting down
)

// HealthCheck holds the result of a single vee-check assertion run inside the VM.
type HealthCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type VMState struct {
	PID int `json:"pid,omitempty"`
	// Backend records which virtualization backend started the VM so stop
	// and control paths dispatch correctly even if the config changes while
	// the VM runs. Empty = QEMU (states written before this field existed).
	Backend       string     `json:"backend,omitempty"`
	QMPSocket     string     `json:"qmp_socket,omitempty"`
	QGASocket     string     `json:"qga_socket,omitempty"`
	SPICEPort     int        `json:"spice_port,omitempty"`
	SSHPort       int        `json:"ssh_port,omitempty"`
	VirtiofsdPIDs []int      `json:"virtiofsd_pids,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	Running       bool       `json:"running"`
	InstallState  string     `json:"install_state,omitempty"`
	InstalledAt   *time.Time `json:"installed_at,omitempty"`

	// DesiredState is what the user last asked for ("running" / "stopped").
	// Empty = legacy state from before this field existed; the daemon treats
	// it as "running" for auto_start VMs to preserve old behaviour.
	DesiredState string `json:"desired_state,omitempty"`

	// LastShutdownReason records why the VM last stopped: user, guest, or
	// crash. The daemon uses this together with DesiredState to decide whether
	// to restart an auto_start VM.
	LastShutdownReason string `json:"last_shutdown_reason,omitempty"`

	// Boot phase tracking — populated by the phase watcher tailing serial.log
	// during the start sequence. Reset to empty on Stop.
	BootPhase      string     `json:"boot_phase,omitempty"`
	PhaseStartedAt *time.Time `json:"phase_started_at,omitempty"`
	LastPanicLine  string     `json:"last_panic_line,omitempty"`

	// PostInstallChecks holds the last run of /usr/local/bin/vee-check results.
	// Empty means the check has not been run yet.
	PostInstallChecks    []HealthCheck `json:"post_install_checks,omitempty"`
	PostInstallCheckedAt *time.Time    `json:"post_install_checked_at,omitempty"`
}

// BackendName resolves the backend that started this VM, defaulting to QEMU
// for nil or legacy states written before the Backend field existed.
func (s *VMState) BackendName() backend.Name {
	if s == nil || s.Backend == "" {
		return backend.QEMU
	}
	return backend.Name(s.Backend)
}
