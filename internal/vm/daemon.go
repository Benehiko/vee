package vm

import (
	"context"
	"time"

	"go.uber.org/zap"
)

const daemonPollInterval = 30 * time.Second

// RunDaemon starts all AutoStart VMs and then watches them, restarting any
// that have exited. It blocks until ctx is cancelled.
func (m *Manager) RunDaemon(ctx context.Context) error {
	log := m.provider.Logger()
	log.Info("vee daemon starting")

	if err := m.startAutoStartVMs(ctx); err != nil {
		log.Warn("initial autostart pass had errors", zap.Error(err))
	}

	ticker := time.NewTicker(daemonPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("vee daemon stopping")
			return nil
		case <-ticker.C:
			if err := m.startAutoStartVMs(ctx); err != nil {
				log.Warn("autostart watch pass had errors", zap.Error(err))
			}
		}
	}
}

// startAutoStartVMs starts any AutoStart VM that is not currently running.
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
