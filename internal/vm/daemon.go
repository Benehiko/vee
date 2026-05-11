package vm

import (
	"context"
	"fmt"
	"time"

	"github.com/Benehiko/vee/internal/notify"
	"github.com/Benehiko/vee/internal/shutdown"
	"go.uber.org/zap"
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
// have exited. It also acquires a logind shutdown inhibitor *while at least
// one VM is running* so the host blocks on power-off/reboot only when there
// is something to wait for. With no VMs running the inhibitor is released,
// letting KDE / systemctl poweroff proceed without the 30s "shutdown
// blocked" abort dialog.
//
// Exit paths:
//   - ctx.Done()         → user/systemd stopped the daemon (e.g. systemctl
//     stop vee). Inhibitor is released; running VMs are
//     intentionally left alone.
//   - host shutdown      → logind PrepareForShutdown(true) fires. Notify
//     the user, gracefully stop all VMs, release the
//     inhibitor, then return so the unit exits.
func (m *Manager) RunDaemon(ctx context.Context) error {
	log := m.provider.Logger()
	log.Info("vee daemon starting")

	conn, err := shutdown.Connect()
	if err != nil {
		log.Warn("could not open logind connection; host shutdown will not wait for VMs",
			zap.Error(err))
	}
	defer func() {
		if conn != nil {
			if err := conn.Close(); err != nil {
				log.Debug("logind connection close failed", zap.Error(err))
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

	if err := m.startAutoStartVMs(ctx); err != nil {
		log.Warn("initial autostart pass had errors", zap.Error(err))
	}
	reconcileInhibitor()

	ticker := time.NewTicker(daemonPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Disambiguate "user ran systemctl stop vee" (leave VMs alone)
			// from "daemon SIGTERMed during host shutdown" (stop VMs).
			// Without this check the select branches race on shutdown:
			// if ctx.Done wins before shutdownCh, VMs survive the daemon
			// only to be SIGKILLed seconds later by the host poweroff.
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

// handleHostShutdown is invoked when logind signals that the host is
// powering off or rebooting. It notifies the user, stops every running VM
// in parallel, and notifies again once done. The inhibitor release in
// RunDaemon is what actually unblocks logind.
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
	_ = ctx // outer ctx may already be cancelled by systemd; use a fresh one for stop work

	if err := m.StopAllRunning(stopCtx, daemonStopPerVMTimeout); err != nil {
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
