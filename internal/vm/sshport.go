package vm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/qemu"
)

// SetSSHPort changes the host port forwarded to guest port 22 after the VM has
// been created. The port is persisted to the config, so every later start uses
// it, and when the VM is currently running on QEMU user-mode NAT it is also
// applied live through QMP (hostfwd_remove + hostfwd_add on the user netdev) —
// no stop/start needed. The returned bool reports whether the live apply
// happened; false with a nil error means the port was saved and takes effect
// on the next start (VM stopped, or bridge mode where the daemon's loopback
// proxy binds the port when it next reconciles the VM).
func (m *Manager) SetSSHPort(ctx context.Context, name string, port int) (bool, error) {
	if port <= 0 || port > 65535 {
		return false, fmt.Errorf("invalid SSH port %d: must be 1-65535", port)
	}
	cfg, err := m.loadConfig(name)
	if err != nil {
		return false, err
	}
	if cfg.BackendName() == backend.VZ {
		return false, fmt.Errorf("the vz backend does not support ssh_port: NAT has no host port-forwarding — use `vee ssh` (resolves the guest IP by MAC)")
	}

	cfg.SSHPort = port
	if err := m.saveConfig(cfg); err != nil {
		return false, err
	}

	state, err := m.loadState(name)
	if err != nil || !state.Running || !isAlive(state.PID) {
		return false, nil
	}
	// A hostfwd only exists through user-mode NAT (#110). On a real bridge the
	// daemon's SSH loopback proxy serves cfg.SSHPort; it picks the new port up
	// when the VM next restarts, so there is nothing to apply live here.
	if effectiveBridge(cfg) {
		return false, nil
	}
	if state.QMPSocket == "" {
		return false, nil
	}

	client, err := qemu.NewQMPClient(ctx, state.QMPSocket, 5*time.Second)
	if err != nil {
		return false, fmt.Errorf("VM is running but its QMP socket is unreachable (port saved for next start): %w", err)
	}
	defer func() { _ = client.Close() }()

	// Drop the forward the running QEMU actually bound (state, not config —
	// Start may have moved off a busy preferred port). Best-effort: a missing
	// forward just makes hostfwd_remove print a complaint we ignore.
	if state.SSHPort > 0 && state.SSHPort != port {
		if out, herr := client.HumanMonitorCommand(fmt.Sprintf("hostfwd_remove net0 tcp:127.0.0.1:%d", state.SSHPort)); herr == nil && strings.TrimSpace(out) != "" {
			m.provider.Logger().Debug("hostfwd_remove", zap.String("vm", name), zap.String("output", strings.TrimSpace(out)))
		}
	}
	out, err := client.HumanMonitorCommand(fmt.Sprintf("hostfwd_add net0 tcp:127.0.0.1:%d-:22", port))
	if err != nil {
		return false, fmt.Errorf("apply port to running VM (port saved for next start): %w", err)
	}
	if msg := strings.TrimSpace(out); msg != "" {
		return false, fmt.Errorf("apply port to running VM (port saved for next start): %s", msg)
	}

	state.SSHPort = port
	if err := m.saveState(name, state); err != nil {
		return true, fmt.Errorf("port forwarded live, but recording it in state failed: %w", err)
	}
	m.provider.Logger().Info("ssh port updated live via QMP",
		zap.String("vm", name), zap.Int("port", port))
	return true, nil
}
