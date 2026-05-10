package vm

import "time"

const (
	InstallStatePending = "pending"
	InstallStateReady   = "ready"
)

type VMState struct {
	PID           int        `json:"pid,omitempty"`
	QMPSocket     string     `json:"qmp_socket,omitempty"`
	QGASocket     string     `json:"qga_socket,omitempty"`
	SPICEPort     int        `json:"spice_port,omitempty"`
	SSHPort       int        `json:"ssh_port,omitempty"`
	VirtiofsdPIDs []int      `json:"virtiofsd_pids,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	Running       bool       `json:"running"`
	InstallState  string     `json:"install_state,omitempty"`
	InstalledAt   *time.Time `json:"installed_at,omitempty"`
}
