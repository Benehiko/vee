package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"
)

// WaitSSHReady blocks until the guest is actually usable over SSH: an
// authenticated exec round-trip succeeds, unlike the start-time readiness
// probe, whose bare TCP dial (or QGA ping) can flip "ready" before
// authorized_keys are in place or cloud-init has finished. With cloudInit set
// it additionally runs cloudInitWaitCmd (POSIX guests only), so "ready" also
// means first-boot provisioning is done.
func (m *Manager) WaitSSHReady(ctx context.Context, name string, timeout time.Duration, cloudInit bool) error {
	cfg, err := m.LoadConfig(name)
	if err != nil {
		return fmt.Errorf("VM %q not found: %w", name, err)
	}
	state, err := m.LoadState(name)
	if err != nil {
		return err
	}
	if state == nil || !state.Running {
		return fmt.Errorf("VM %q is not running", name)
	}

	user := cfg.SSHUsername()
	if user == "" && cfg.Template == "truenas" {
		user = cfg.TrueNASUser
	}
	if user == "" {
		return fmt.Errorf("VM %q has no SSH account recorded; pass one with vee ssh --user instead", name)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	key, err := readVeePrivateKey(filepath.Join(home, ".vee", "ssh", "id_ed25519"))
	if err != nil {
		return err
	}

	// `ver` is a cmd.exe builtin and `true` a POSIX one; both take no
	// arguments, so neither needs the guest-shell quoting vee ssh applies to
	// user commands (cmd/ssh.go).
	probe := "true"
	windows := cfg.WindowsGuest()
	if windows {
		probe = "ver"
	}

	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			if lastErr == nil {
				lastErr = context.DeadlineExceeded
			}
			return fmt.Errorf("VM %q not SSH-ready after %s: %w", name, timeout, lastErr)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Reload state each attempt: a guest-requested reboot power-cycles the
		// VM into a new process mid-wait, and the pre-reboot snapshot would
		// misreport the relaunched guest as exited.
		if s, serr := m.loadState(name); serr == nil && s != nil {
			state = s
		}
		if !state.Running || !isAlive(state.PID) {
			if state.LastShutdownReason == ShutdownReasonReboot {
				lastErr = fmt.Errorf("VM %q is power-cycling after a guest-requested reboot", name)
				time.Sleep(2 * time.Second)
				continue
			}
			return fmt.Errorf("VM %q process exited while waiting", name)
		}

		// Re-resolve each attempt: a booting guest's address appears in the
		// lease/neighbour tables late.
		host, port, err := m.guestSSHEndpoint(ctx, cfg, state)
		if err != nil {
			lastErr = err
			time.Sleep(2 * time.Second)
			continue
		}
		// dialSSH keeps retrying internally until its timeout, matching the
		// host-key posture of the CLI (see sshExecClient's doc).
		addr := fmt.Sprintf("%s:%d", host, port)
		client, err := dialSSH(ctx, addr, user, key, min(15*time.Second, remaining))
		if err != nil {
			lastErr = err
			continue
		}

		runCtx, cancel := context.WithTimeout(ctx, min(20*time.Second, remaining))
		_, _, runErr := client.Run(runCtx, probe)
		cancel()
		if runErr != nil {
			_ = client.Close()
			lastErr = runErr
			time.Sleep(2 * time.Second)
			continue
		}

		if cloudInit && !windows {
			err := m.waitCloudInitDone(ctx, client, time.Until(deadline))
			_ = client.Close()
			if err == nil {
				return nil
			}
			if errors.Is(err, errCloudInitFailed) || ctx.Err() != nil {
				return fmt.Errorf("VM %q: %w", name, err)
			}
			// The connection died mid-wait, not cloud-init: first-boot
			// provisioning can bounce the guest's networking (the desktop
			// install restarts systemd-networkd under it). Redial and
			// re-enter the wait until the deadline.
			lastErr = fmt.Errorf("cloud-init wait interrupted: %w", err)
			time.Sleep(2 * time.Second)
			continue
		}
		_ = client.Close()
		return nil
	}
}

// authProbeSpec reports the account and exec command the readiness probe can
// use to prove a guest is actually usable over SSH. Only guests vee's key is
// known to log into qualify: cloud-init-provisioned templates (which always
// inject the vee key). Imported disks carry no account, and truenas uses a
// password admin until backup injects the key — both stay on the weaker
// reachability floor.
func authProbeSpec(cfg *VMConfig) (user, cmd string, ok bool) {
	if cfg == nil || cfg.Template == "truenas" {
		return "", "", false
	}
	user = cfg.SSHUsername()
	if user == "" {
		return "", "", false
	}
	cmd = "true"
	if cfg.WindowsGuest() {
		cmd = "ver"
	}
	return user, cmd, true
}

// sshExecProbe reports whether one authenticated SSH exec round-trip to addr
// succeeds. Budgets are per-tick: the caller polls, so a slow guest just
// fails this attempt and is probed again.
func (m *Manager) sshExecProbe(ctx context.Context, addr, user string, key []byte, cmd string) bool {
	client, err := dialSSH(ctx, addr, user, key, 4*time.Second)
	if err != nil {
		return false
	}
	defer func() { _ = client.Close() }()
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _, err = client.Run(runCtx, cmd)
	return err == nil
}

// cloudInitWaitCmd gates readiness on first-boot provisioning. Guests without
// cloud-init (macOS, Alpine-based templates) exit 127 up front, which cannot
// block readiness. Where passwordless sudo works, status runs privileged: on
// Fedora /run/cloud-init/status.json is root-readable only, so an
// unprivileged `cloud-init status` exits 1 — a spurious hard failure, not
// "still provisioning".
const cloudInitWaitCmd = "command -v cloud-init >/dev/null 2>&1 || exit 127; " +
	"if sudo -n true 2>/dev/null; then sudo -n cloud-init status --wait; else cloud-init status --wait; fi"

// errCloudInitFailed marks a definitive provisioning failure: cloud-init ran
// to completion and reported error status. Only this outcome is terminal for
// the wait — a dropped connection is retried on a fresh dial, because
// first-boot provisioning can bounce the guest's networking under the wait.
var errCloudInitFailed = errors.New("cloud-init finished with an error status")

// waitCloudInitDone runs cloudInitWaitCmd on an established connection,
// bounded by the caller's remaining time. Exit status 2 means done with
// recoverable errors — still done; 127 means the guest has no cloud-init,
// which cannot block readiness either. Any other exit status is a terminal
// errCloudInitFailed; a connection-level failure (no exit status) is returned
// as-is for the caller to retry.
func (m *Manager) waitCloudInitDone(ctx context.Context, client *sshExecClient, remaining time.Duration) error {
	runCtx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	_, _, err := client.Run(runCtx, cloudInitWaitCmd)
	if err == nil {
		return nil
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		switch exitErr.ExitStatus() {
		case 2, 127:
			return nil
		}
		return fmt.Errorf("%w (cloud-init status --wait exited %d)", errCloudInitFailed, exitErr.ExitStatus())
	}
	return fmt.Errorf("cloud-init not finished: %w", err)
}
