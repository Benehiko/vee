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
	daemonPollInterval     = 30 * time.Second
	daemonStopPerVMTimeout = 60 * time.Second
)

// RunDaemon starts all AutoStart VMs and watches them, restarting any that
// have exited. It also acquires a logind shutdown inhibitor so the host
// will block on power-off/reboot until vee has gracefully stopped every
// running VM.
//
// Exit paths:
//   - ctx.Done()         → user/systemd stopped the daemon (e.g. systemctl
//     --user stop vee). Inhibitor is released; running
//     VMs are intentionally left alone.
//   - host shutdown      → logind PrepareForShutdown(true) fires. Notify
//     the user, gracefully stop all VMs, release the
//     inhibitor, then return so the unit exits.
func (m *Manager) RunDaemon(ctx context.Context) error {
	log := m.provider.Logger()
	log.Info("vee daemon starting")

	inhibitor, err := shutdown.Acquire("vee", "Gracefully shutting down running VMs")
	if err != nil {
		log.Warn("could not acquire shutdown inhibitor; host shutdown will not wait for VMs",
			zap.Error(err))
	} else {
		log.Info("shutdown inhibitor acquired")
		defer func() {
			if err := inhibitor.Release(); err != nil {
				log.Warn("inhibitor release failed", zap.Error(err))
			} else {
				log.Info("shutdown inhibitor released")
			}
		}()
	}

	var shutdownCh <-chan struct{}
	if inhibitor != nil {
		ch, subErr := inhibitor.PrepareForShutdown(ctx)
		if subErr != nil {
			log.Warn("PrepareForShutdown subscription failed", zap.Error(subErr))
		} else {
			shutdownCh = ch
		}
	}

	if err := m.startAutoStartVMs(ctx); err != nil {
		log.Warn("initial autostart pass had errors", zap.Error(err))
	}

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
			if inhibitor != nil {
				if preparing, perr := inhibitor.PreparingForShutdown(); perr == nil && preparing {
					log.Info("ctx cancelled during host shutdown; stopping VMs")
					m.handleHostShutdown(ctx)
					return nil
				}
			}
			log.Info("vee daemon stopping (context cancelled)")
			return nil
		case <-shutdownCh:
			m.handleHostShutdown(ctx)
			return nil
		case <-ticker.C:
			if err := m.startAutoStartVMs(ctx); err != nil {
				log.Warn("autostart watch pass had errors", zap.Error(err))
			}
		}
	}
}

// handleHostShutdown is invoked when logind signals that the host is
// powering off or rebooting. It notifies the user, stops every running VM
// in parallel, and notifies again once done. The deferred inhibitor.Release
// in RunDaemon is what actually unblocks logind.
func (m *Manager) handleHostShutdown(ctx context.Context) {
	log := m.provider.Logger()
	log.Info("host shutdown signal received; stopping running VMs")

	entries, _ := m.List()
	var runningCount int
	for _, e := range entries {
		if e.State != nil && e.State.Running && isAlive(e.State.PID) {
			runningCount++
		}
	}

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
