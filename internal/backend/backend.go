// Package backend defines the narrow contract the VM manager requires from a
// virtualization backend. QEMU is the default backend for every guest;
// additional backends (Apple Virtualization.framework, issue #51) plug in
// behind the same interfaces. Backend selection is per-VM and orthogonal to
// the guest OS, so a guest that runs on one backend today can gain another
// later (e.g. macOS guests on QEMU vmapple once upstream recovers, issue #50).
//
// Runtime control (graceful stop, shutdown-event watching, guest exec, stats)
// is dispatched per-backend inside the manager from persisted VM state, not
// through this interface: a stop may run in a different process than the one
// that started the VM, so there is no live Machine value to call. Capability
// interfaces for those operations will be added here together with the second
// backend implementation, once their real shape is known.
package backend

import "context"

// Name identifies a virtualization backend in VM configs and state files.
type Name string

const (
	// QEMU drives VMs through a detached qemu-system process. It is the
	// default: configs written before the backend field existed carry an
	// empty value and resolve to QEMU.
	QEMU Name = "qemu"
	// VZ drives macOS guests through Apple's Virtualization.framework via a
	// helper process (Apple Silicon hosts only). See issue #51.
	VZ Name = "vz"
)

// Valid reports whether n names a known backend. The empty string is valid
// and resolves to QEMU.
func Valid(n Name) bool {
	switch n {
	case "", QEMU, VZ:
		return true
	}
	return false
}

// StartResult holds the outcome of a detached VM start in backend-neutral
// terms.
type StartResult struct {
	// PID of the detached process that owns the VM (qemu-system, or the vz
	// helper). Liveness checks, stop and force-stop all key off it.
	PID int
	// ControlSocket is the per-VM runtime control channel: the QMP unix
	// socket for QEMU, the helper control socket for vz. Empty when the
	// backend exposes none.
	ControlSocket string
	// GuestAgentSocket is the QGA unix socket for QEMU guests running
	// qemu-guest-agent. Empty for backends without a guest agent.
	GuestAgentSocket string
	// LeaseBaseline is the guest's DHCP-lease expiry as it was immediately
	// before the VM started, for backends whose readiness signal is the guest
	// acquiring a lease. Zero when unused.
	LeaseBaseline uint64
}

// Machine is a fully-configured VM ready to launch — the only surface the
// manager touches between building a machine and persisting its state.
// Implementations must detach the VM process (it survives the caller's exit)
// and keep every artifact they write under the VM's storage directory so
// delete and backup semantics hold across backends.
type Machine interface {
	StartDetached(ctx context.Context) (*StartResult, error)
}
