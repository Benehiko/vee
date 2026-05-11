package vm

import (
	"encoding/json"
	"time"

	"github.com/Benehiko/vee/internal/qemu"
	"go.uber.org/zap"
)

// shutdownWatcherDialTimeout bounds how long the watcher will wait for the
// QMP socket to come up before giving up. The Start path already waited for
// the same socket, so this only covers the goroutine startup race.
const shutdownWatcherDialTimeout = 5 * time.Second

// watchShutdownEvents opens a dedicated QMP event-listener connection and
// records LastShutdownReason in state.json when the guest issues a SHUTDOWN
// event. It is best-effort: any failure (dial, decode, daemon restart that
// kills this goroutine) leaves the reason unset, and the eventual stale-VM
// cleanup will record "crash" instead.
//
// The watcher exits when:
//   - it observes a SHUTDOWN event and updates state, or
//   - the QMP connection closes (QEMU exited before we got a SHUTDOWN), or
//   - the QMP dial fails outright.
func (m *Manager) watchShutdownEvents(name, qmpSocket string) {
	log := m.provider.Logger()
	if qmpSocket == "" {
		return
	}

	client, err := qemu.NewQMPEventListener(qmpSocket, shutdownWatcherDialTimeout)
	if err != nil {
		log.Debug("shutdown watcher: QMP dial failed",
			zap.String("vm", name), zap.Error(err))
		return
	}
	defer func() { _ = client.Close() }()

	for {
		ev, err := client.ReadEvent()
		if err != nil {
			log.Debug("shutdown watcher: read loop ended",
				zap.String("vm", name), zap.Error(err))
			return
		}
		if ev.Event != "SHUTDOWN" {
			continue
		}
		var data qemu.ShutdownEventData
		_ = json.Unmarshal(ev.Data, &data)

		reason := ShutdownReasonGuest
		if !data.Guest {
			// Host-initiated: either our own Stop (which sets reason=user
			// before we see this event) or an external SIGTERM. Don't
			// overwrite — let Stop's reason stand, and treat any unset
			// case as a crash via cleanupStaleVM.
			log.Debug("shutdown watcher: host-initiated SHUTDOWN, leaving reason untouched",
				zap.String("vm", name))
			return
		}

		state, loadErr := m.loadState(name)
		if loadErr != nil {
			log.Debug("shutdown watcher: loadState failed",
				zap.String("vm", name), zap.Error(loadErr))
			return
		}
		// Don't clobber an explicit user-stop reason if it raced ahead.
		if state.LastShutdownReason == "" {
			state.LastShutdownReason = reason
		}
		// Mark the VM as not-desired-to-run so the daemon won't autostart
		// it after a guest-initiated poweroff.
		state.DesiredState = DesiredStateStopped
		if err := m.saveState(name, state); err != nil {
			log.Warn("shutdown watcher: saveState failed",
				zap.String("vm", name), zap.Error(err))
			return
		}
		log.Info("shutdown watcher: recorded guest-initiated shutdown",
			zap.String("vm", name))
		return
	}
}
