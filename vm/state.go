package vm

import "time"

type VMState struct {
	PID          int       `json:"pid,omitempty"`
	QMPSocket    string    `json:"qmp_socket,omitempty"`
	SPICEPort    int       `json:"spice_port,omitempty"`
	VirtiofsdPID int       `json:"virtiofsd_pid,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	Running      bool      `json:"running"`
}
