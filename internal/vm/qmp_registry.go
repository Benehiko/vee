package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/qemu"
)

// qmpRegistry tracks the live QMPOwner for each running VM in the process that
// owns the connections (the daemon). QEMU's QMP socket accepts one client at a
// time, so the owner of that single connection must also be the one that
// executes commands on behalf of `vee qmp`. The registry lets the daemon's
// control socket look up the owner by VM name and route a command to it.
//
// A nil or empty registry (e.g. a short-lived CLI process that started a VM
// directly) simply has no entries; callers fall back to dialing the QMP socket
// themselves.
type qmpRegistry struct {
	mu     sync.RWMutex
	owners map[string]*qemu.QMPOwner
}

func newQMPRegistry() *qmpRegistry {
	return &qmpRegistry{owners: make(map[string]*qemu.QMPOwner)}
}

func (r *qmpRegistry) set(name string, o *qemu.QMPOwner) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owners[name] = o
}

func (r *qmpRegistry) get(name string) (*qemu.QMPOwner, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o, ok := r.owners[name]
	return o, ok
}

func (r *qmpRegistry) delete(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.owners, name)
}

// ExecuteQMP runs a QMP command against a running VM via its registered owner
// connection. It returns errQMPNotOwned if this process does not own the VM's
// QMP connection (so the caller can fall back to a direct dial).
func (m *Manager) ExecuteQMP(name, execute string, args map[string]any) (json.RawMessage, error) {
	if m.qmp == nil {
		return nil, errQMPNotOwned
	}
	owner, ok := m.qmp.get(name)
	if !ok {
		return nil, errQMPNotOwned
	}
	return owner.Execute(execute, args)
}

// errQMPNotOwned signals that this process holds no QMP connection for the VM.
var errQMPNotOwned = fmt.Errorf("QMP connection not owned by this process")

// IsErrQMPNotOwned reports whether err indicates the VM's QMP connection is not
// owned here (caller should dial directly instead).
func IsErrQMPNotOwned(err error) bool { return err == errQMPNotOwned }

// powerdown issues system_powerdown to a VM, preferring the owned connection
// (which the daemon holds for the VM's lifetime) and falling back to a direct
// dial when this process owns no connection for the VM — e.g. a standalone
// `vee stop` with no daemon running. Best-effort: errors are swallowed because
// the caller follows up with a SIGKILL fallback if the process does not exit.
func (m *Manager) powerdown(ctx context.Context, name, qmpSocket string) {
	// 1. This process owns the connection (the daemon stopping its own VMs).
	if _, err := m.ExecuteQMP(name, "system_powerdown", nil); err == nil {
		return
	} else if !IsErrQMPNotOwned(err) {
		// Owned connection exists but the command failed; nothing more to try —
		// the SIGKILL fallback in Stop handles a wedged VM.
		return
	}

	// 2. A daemon in another process may own the connection (CLI `vee stop`
	//    while the daemon supervises the VM). Route through its control socket.
	if _, reachable, err := m.QMPViaDaemon(ctx, name, "system_powerdown", nil); reachable {
		if err == nil {
			return
		}
		// Daemon answered but the command failed; fall through to a direct dial
		// as a last resort in case the daemon's connection is wedged.
	}

	// 3. No daemon owns it — dial the QMP socket directly.
	client, dialErr := qemu.NewQMPClient(ctx, qmpSocket, 3*time.Second)
	if dialErr != nil {
		return
	}
	_ = client.SystemPowerdown()
	_ = client.Close()
}

// watchVMConnection opens the single QMP owner connection for a VM, registers
// it so commands can be routed to it, and processes SHUTDOWN events to record
// LastShutdownReason. It replaces the old event-only watcher: the same
// connection now serves both events and commands.
//
// It is best-effort and blocks until the connection drops or a terminal
// SHUTDOWN is handled, then deregisters the owner. Callers run it in a
// goroutine that outlives the Start call.
func (m *Manager) watchVMConnection(ctx context.Context, name, qmpSocket string) {
	log := m.provider.Logger()
	if qmpSocket == "" {
		return
	}

	events := make(chan qemu.QMPEvent, 8)
	owner, err := qemu.NewQMPOwner(ctx, qmpSocket, shutdownWatcherDialTimeout, func(ev qemu.QMPEvent) {
		// Runs on the owner's reader goroutine; hand off without blocking.
		select {
		case events <- ev:
		default:
		}
	})
	if err != nil {
		log.Debug("QMP owner: dial failed",
			zap.String("vm", name), zap.Error(err))
		return
	}
	if m.qmp != nil {
		m.qmp.set(name, owner)
	}
	defer func() {
		if m.qmp != nil {
			m.qmp.delete(name)
		}
		_ = owner.Close()
	}()

	for {
		select {
		case <-owner.Done():
			log.Debug("QMP owner: connection closed",
				zap.String("vm", name))
			return
		case ev := <-events:
			if ev.Event != "SHUTDOWN" {
				continue
			}
			if m.handleShutdownEvent(name, ev) {
				return
			}
		}
	}
}

// handleShutdownEvent records the shutdown reason for a SHUTDOWN event and
// reports whether the watcher should stop (true once the terminal event is
// handled).
func (m *Manager) handleShutdownEvent(name string, ev qemu.QMPEvent) bool {
	log := m.provider.Logger()

	var data qemu.ShutdownEventData
	_ = json.Unmarshal(ev.Data, &data)

	if !data.Guest {
		// Host-initiated: our own Stop set reason=user already, or an external
		// SIGTERM. Leave the reason to Stop / stale-VM cleanup.
		log.Debug("QMP owner: host-initiated SHUTDOWN, leaving reason untouched",
			zap.String("vm", name))
		return true
	}

	state, loadErr := m.loadState(name)
	if loadErr != nil {
		log.Debug("QMP owner: loadState failed",
			zap.String("vm", name), zap.Error(loadErr))
		return true
	}
	if state.LastShutdownReason == "" {
		state.LastShutdownReason = ShutdownReasonGuest
	}
	state.DesiredState = DesiredStateStopped
	if err := m.saveState(name, state); err != nil {
		log.Warn("QMP owner: saveState failed",
			zap.String("vm", name), zap.Error(err))
		return true
	}
	log.Info("QMP owner: recorded guest-initiated shutdown",
		zap.String("vm", name))
	return true
}

// adoptRunningVMs opens owner connections for VMs that are already running when
// this process (the daemon) starts, so their QMP sockets are owned here and
// `vee qmp` can route to them. Without this, a VM started by a previous daemon
// incarnation or a standalone `vee start` would have no owner in the current
// daemon.
func (m *Manager) adoptRunningVMs(ctx context.Context) {
	entries, err := m.List()
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.State == nil || !e.State.Running || e.State.QMPSocket == "" {
			continue
		}
		if !isAlive(e.State.PID) {
			continue
		}
		if m.qmp != nil {
			if _, ok := m.qmp.get(e.Config.Name); ok {
				continue
			}
		}
		go m.watchVMConnection(context.WithoutCancel(ctx), e.Config.Name, e.State.QMPSocket)
	}
}

// waitOwner polls the registry briefly for a VM's owner to appear. Adoption is
// asynchronous (each connection dials in its own goroutine), so a command that
// arrives immediately after daemon start may race the owner registration.
func (m *Manager) waitOwner(name string, timeout time.Duration) bool {
	if m.qmp == nil {
		return false
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, ok := m.qmp.get(name); ok {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, ok := m.qmp.get(name)
	return ok
}
