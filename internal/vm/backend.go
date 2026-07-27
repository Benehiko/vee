package vm

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/qemu"
	"github.com/Benehiko/vee/internal/vzhelper"
)

// buildBackendMachine dispatches machine construction on the VM's backend.
// The QEMU path is the existing buildMachine; other backends slot in here.
// The returned PIDs are virtiofsd helpers — a QEMU-only concern (vz shares
// directories natively).
func (m *Manager) buildBackendMachine(ctx context.Context, cfg *VMConfig) (backend.Machine, []int, error) {
	switch cfg.BackendName() {
	case backend.QEMU:
		machine, virtiofsdPIDs, err := m.buildMachine(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		return qemuMachine{machine}, virtiofsdPIDs, nil
	case backend.VZ:
		machine, err := m.buildVZMachine(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		return machine, nil, nil
	default:
		return nil, nil, fmt.Errorf("unknown backend %q (valid: %q, %q)", cfg.Backend, backend.QEMU, backend.VZ)
	}
}

// qemuMachine adapts *qemu.BaseMachine to the backend-neutral Machine
// interface.
type qemuMachine struct {
	machine *qemu.BaseMachine
}

func (q qemuMachine) StartDetached(ctx context.Context) (*backend.StartResult, error) {
	res, err := q.machine.StartDetached(ctx)
	if err != nil {
		return nil, err
	}
	return &backend.StartResult{
		PID:              res.PID,
		ControlSocket:    res.QMPSocket,
		GuestAgentSocket: res.QGASocket,
	}, nil
}

// assignControlSockets maps a backend-neutral StartResult onto the typed
// VMState socket fields. QMP consumers (vee qmp, monitor, dashboard) key off
// QMPSocket, so a vz control socket must never land there.
func assignControlSockets(state *VMState, cfg *VMConfig, result *backend.StartResult) {
	switch cfg.BackendName() {
	case backend.VZ:
		state.ControlSocket = result.ControlSocket
	default:
		state.QMPSocket = result.ControlSocket
		state.QGASocket = result.GuestAgentSocket
	}
}

// gracefulShutdown asks the guest to power down via its backend's control
// channel. Best-effort: callers follow up with a SIGKILL when the process
// does not exit in time.
func (m *Manager) gracefulShutdown(ctx context.Context, name string, state *VMState) {
	switch state.BackendName() {
	case backend.VZ:
		// Ask the helper to requestStop the guest (the ACPI-powerdown
		// analog). Best-effort like the QMP path.
		if state.ControlSocket != "" {
			if _, err := vzControlRequest(ctx, state.ControlSocket, vzhelper.OpStop, 5*time.Second); err != nil {
				m.provider.Logger().Debug("vz graceful stop failed",
					zap.String("vm", name), zap.Error(err))
			}
		}
	default:
		// QEMU — and any unrecognized backend value (hand-edited state,
		// version skew): preserve the legacy best-effort QMP powerdown for
		// any state that recorded a QMP socket rather than downgrading
		// straight to the SIGKILL fallback.
		if state.QMPSocket != "" {
			m.powerdown(ctx, name, state.QMPSocket)
		}
	}
}
