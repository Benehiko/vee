package vm

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/notify"
	"github.com/Benehiko/vee/internal/shutdown"
)

const (
	// daemonPollInterval drives both autostart-recovery and inhibitor
	// reconciliation. Kept short so releasing the shutdown inhibitor after
	// the last VM stops does not leave the host blocked for long if the
	// user immediately tries to power off.
	daemonPollInterval     = 5 * time.Second
	daemonStopPerVMTimeout = 60 * time.Second
)

// RunDaemon starts all AutoStart VMs and watches them, restarting any that
// have exited. It also acquires a shutdown inhibitor *while at least one VM
// is running* so the host blocks on power-off/reboot only when there is
// something to wait for. With no VMs running the inhibitor is released,
// letting KDE / systemctl poweroff proceed without the 30s "shutdown
// blocked" abort dialog.
//
// Host-shutdown detection is platform-specific behind internal/shutdown:
// logind's PrepareForShutdown signal and a block inhibitor on Linux;
// launchd's SIGTERM-at-shutdown contract on macOS, where the inhibitor is
// a caffeinate sleep assertion and *every* SIGTERM reports
// PreparingForShutdown=true because launchd offers no way to tell a plain
// stop from a host power-off.
//
// Exit paths:
//   - ctx.Done()         → user/service manager stopped the daemon (e.g.
//     systemctl stop vee, SIGINT). Inhibitor is
//     released; running VMs are intentionally left
//     alone — except on macOS, where SIGTERM takes
//     the host-shutdown path below.
//   - host shutdown      → PrepareForShutdown fires (or ctx.Done reports a
//     shutdown in progress). Notify the user,
//     gracefully stop all VMs, release the inhibitor,
//     then return so the service exits.
func (m *Manager) RunDaemon(ctx context.Context) error {
	log := m.provider.Logger()
	log.Info("vee daemon starting")

	conn, err := shutdown.Connect()
	if err != nil {
		log.Warn("host shutdown integration unavailable; host shutdown will not wait for VMs",
			zap.Error(err))
	}
	defer func() {
		if conn != nil {
			if err := conn.Close(); err != nil {
				log.Debug("shutdown monitor close failed", zap.Error(err))
			}
		}
	}()

	var shutdownCh <-chan struct{}
	if conn != nil {
		ch, subErr := conn.PrepareForShutdown(ctx)
		if subErr != nil {
			log.Warn("PrepareForShutdown subscription failed", zap.Error(subErr))
		} else {
			shutdownCh = ch
		}
	}

	var lock *shutdown.Lock
	releaseLock := func(reason string) {
		if lock == nil {
			return
		}
		if err := lock.Release(); err != nil {
			log.Warn("inhibitor release failed", zap.Error(err))
		} else {
			log.Info("shutdown inhibitor released", zap.String("reason", reason))
		}
		lock = nil
	}
	defer releaseLock("daemon exit")

	// reconcile inhibitor state to match running VM count
	reconcileInhibitor := func() {
		if conn == nil {
			return
		}
		running := m.runningVMCount()
		switch {
		case running > 0 && lock == nil:
			l, err := conn.Acquire("vee", "Gracefully shutting down running VMs")
			if err != nil {
				log.Warn("could not acquire shutdown inhibitor; host shutdown will not wait for VMs",
					zap.Error(err))
				return
			}
			lock = l
			log.Info("shutdown inhibitor acquired", zap.Int("running_vms", running))
		case running == 0 && lock != nil:
			releaseLock("no VMs running")
		}
	}

	// Serve the control socket so `vee qmp` can route commands to the QMP
	// connections this daemon owns.
	go m.serveControlSocket(ctx)

	// Adopt VMs already running (e.g. started before this daemon incarnation)
	// so their QMP sockets are owned here and reachable via the control socket.
	m.adoptRunningVMs(ctx)

	if err := m.startAutoStartVMs(ctx); err != nil {
		log.Warn("initial autostart pass had errors", zap.Error(err))
	}
	reconcileInhibitor()
	m.reconcileSSHProxies(ctx)
	defer m.stopAllSSHProxies()
	m.reconcileTunnels(ctx)
	defer m.stopAllTunnels()

	// The vhost router publishes HTTP background tunnels under
	// <service>.<host> names. It is best-effort: binding :80 needs
	// CAP_NET_BIND_SERVICE, and a host without it still gets working
	// per-port tunnels.
	go m.serveTunnelRouter(ctx)

	ticker := time.NewTicker(daemonPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Disambiguate "user stopped the daemon" (leave VMs alone)
			// from "daemon SIGTERMed during host shutdown" (stop VMs).
			// Without this check the select branches race on shutdown:
			// if ctx.Done wins before shutdownCh, VMs survive the daemon
			// only to be SIGKILLed seconds later by the host poweroff.
			// On macOS PreparingForShutdown is true for any SIGTERM —
			// launchd has no separate shutdown notification.
			if conn != nil {
				if preparing, perr := conn.PreparingForShutdown(); perr == nil && preparing {
					log.Info("ctx cancelled during host shutdown; stopping VMs")
					m.handleHostShutdown(ctx)
					releaseLock("host shutdown complete")
					return nil
				}
			}
			log.Info("vee daemon stopping (context cancelled)")
			return nil
		case <-shutdownCh:
			m.handleHostShutdown(ctx)
			releaseLock("host shutdown complete")
			return nil
		case <-ticker.C:
			if err := m.startAutoStartVMs(ctx); err != nil {
				log.Warn("autostart watch pass had errors", zap.Error(err))
			}
			reconcileInhibitor()
			m.reconcileSSHProxies(ctx)
			m.reconcileTunnels(ctx)
		}
	}
}

// runningVMCount reports how many VMs are currently alive per state +
// kernel pid check.
func (m *Manager) runningVMCount() int {
	entries, err := m.List()
	if err != nil {
		return 0
	}
	var n int
	for _, e := range entries {
		if e.State != nil && e.State.Running && isAlive(e.State.PID) {
			n++
		}
	}
	return n
}

// handleHostShutdown is invoked when the host is powering off or rebooting
// (logind's PrepareForShutdown on Linux, launchd's SIGTERM on macOS). It
// notifies the user, stops every running VM in parallel, and notifies again
// once done. On Linux the inhibitor release in RunDaemon is what actually
// unblocks logind; on macOS the daemon exiting within the launchd job's
// ExitTimeOut is what lets shutdown proceed.
func (m *Manager) handleHostShutdown(ctx context.Context) {
	log := m.provider.Logger()
	log.Info("host shutdown signal received; stopping running VMs")

	runningCount := m.runningVMCount()
	if runningCount == 0 {
		log.Info("no running VMs at shutdown; releasing inhibitor")
		return
	}

	body := fmt.Sprintf(
		"Powering off %d running VM(s) before host shutdown.\n"+
			"Override with: vee stop --force <name>",
		runningCount,
	)
	if err := notify.Send("vee: shutting down VMs", body, notify.UrgencyCritical); err != nil {
		log.Debug("notify (start) failed", zap.Error(err))
	}

	stopCtx, cancel := context.WithTimeout(
		context.Background(),
		daemonStopPerVMTimeout+30*time.Second,
	)
	defer cancel()
	_ = ctx // outer ctx may already be cancelled by the service manager; use a fresh one for stop work

	//nolint:contextcheck // deliberately uses a fresh ctx: the inherited ctx is already cancelled by the service manager during shutdown, so stop work must not inherit its cancellation
	if err := m.StopAllRunning(stopCtx, daemonStopPerVMTimeout, ShutdownReasonHost); err != nil {
		log.Warn("some VMs did not stop cleanly", zap.Error(err))
		_ = notify.Send(
			"vee: shutdown completed with errors",
			"One or more VMs did not stop cleanly. See ~/.vee/logs/vee.log.",
			notify.UrgencyCritical,
		)
		return
	}

	if err := notify.Send(
		"vee: VMs stopped",
		"All VMs powered off. Host shutdown proceeding.",
		notify.UrgencyNormal,
	); err != nil {
		log.Debug("notify (done) failed", zap.Error(err))
	}
}

// startAutoStartVMs starts any AutoStart VM that is not currently running
// AND that the user has not explicitly stopped (DesiredState=stopped) or
// shut down from inside the guest (LastShutdownReason=guest).
//
// Empty DesiredState is treated as legacy/first-boot and honours the
// auto_start flag so existing setups keep working after upgrade.
func (m *Manager) startAutoStartVMs(ctx context.Context) error {
	cfgs, err := m.ListAutoStart()
	if err != nil {
		return err
	}

	log := m.provider.Logger()
	var lastErr error

	for _, cfg := range cfgs {
		state, stateErr := m.loadState(cfg.Name)
		if stateErr == nil && state.Running && isAlive(state.PID) {
			continue
		}

		if stateErr == nil && !shouldDaemonStart(state) {
			log.Debug("daemon skipping VM (user-stopped or guest-shutdown)",
				zap.String("vm", cfg.Name),
				zap.String("desired_state", state.DesiredState),
				zap.String("last_shutdown_reason", state.LastShutdownReason))
			continue
		}

		log.Info("daemon starting VM", zap.String("vm", cfg.Name))
		if startErr := m.Start(ctx, cfg.Name, false); startErr != nil {
			log.Error("daemon failed to start VM",
				zap.String("vm", cfg.Name),
				zap.Error(startErr))
			lastErr = startErr
		}
	}

	return lastErr
}

// shouldDaemonStart returns true if the daemon's autostart loop should
// (re)start this VM. It respects explicit user intent recorded in state:
//
//   - DesiredState=stopped     → never restart (user ran `vee stop`)
//   - LastShutdownReason=guest → never restart (guest OS shut itself down)
//   - DesiredState=running     → restart (recover from crash / fresh boot)
//   - DesiredState=""          → legacy state predating these fields; honour
//     auto_start as before
func shouldDaemonStart(state *VMState) bool {
	if state == nil {
		return true
	}
	if state.DesiredState == DesiredStateStopped {
		return false
	}
	if state.LastShutdownReason == ShutdownReasonGuest {
		return false
	}
	return true
}
