package vm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// GuestRun runs one command in a running guest over SSH with the vee-managed
// key and returns its stdout. The command goes to the guest account's login
// shell as-is — cmd.exe on Windows guests — so callers do their own quoting
// (Windows callers use EncodedPowerShell to sidestep cmd.exe quoting
// entirely). A non-zero remote exit status is an error carrying stderr.
func (m *Manager) GuestRun(ctx context.Context, name, command string, timeout time.Duration) ([]byte, error) {
	cfg, err := m.LoadConfig(name)
	if err != nil {
		return nil, fmt.Errorf("VM %q not found: %w", name, err)
	}
	state, err := m.LoadState(name)
	if err != nil {
		return nil, err
	}
	if state == nil || !state.Running || !isAlive(state.PID) {
		return nil, fmt.Errorf("VM %q is not running", name)
	}
	user := cfg.SSHUsername()
	if user == "" {
		return nil, fmt.Errorf("VM %q has no SSH account recorded", name)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	key, err := readVeePrivateKey(filepath.Join(home, ".vee", "ssh", "id_ed25519"))
	if err != nil {
		return nil, err
	}
	host, port, err := m.guestSSHEndpoint(ctx, cfg, state)
	if err != nil {
		return nil, err
	}
	client, err := dialSSH(ctx, fmt.Sprintf("%s:%d", host, port), user, key, 15*time.Second)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, _, err := client.Run(runCtx, command)
	return stdout, err
}
