package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/internal/vzhelper"
)

// Bounds for completing the Screen Sharing grant at the end of a create.
const (
	// firstBootWait is how long to wait for the guest's provisioning script to
	// finish. It creates an account, installs a key and starts two services, so
	// a slow first boot on a busy host can take a few minutes.
	firstBootWait = 6 * time.Minute
	// firstBootPoll is the gap between checks for the completion marker.
	firstBootPoll = 5 * time.Second
	// firstBootProbeTimeout bounds one probe. Early in a first boot sshd is not
	// listening yet, so most probes fail fast and are retried.
	firstBootProbeTimeout = 10 * time.Second
	// secondBootWait bounds the wait for the guest to come back after the
	// restart. It matches what `vee start` allows a macOS guest.
	secondBootWait = 10 * time.Minute
)

var (
	// errFirstBootIncomplete reports that the guest did not finish provisioning
	// in time, so it must not be stopped yet.
	errFirstBootIncomplete = errors.New("guest has not finished its first-boot provisioning")
	// errGuestExited reports that the guest stopped running while being waited
	// for, so waiting longer is pointless.
	errGuestExited = errors.New("guest stopped running before it finished provisioning")
	// ErrGuestLeftStopped reports that the guest was stopped for the grant and
	// could not be started again. The caller must say so: the user's VM is off,
	// which is not what they asked for.
	ErrGuestLeftStopped = errors.New("the guest was stopped to authorize Screen Sharing and could not be started again")
)

// Seams for the create-time sequence. Its whole value is the ORDER of these
// steps — wait for provisioning, stop, start, wait again — and none of them can
// run against a real guest in a test, so they are swappable.
var (
	waitFirstBoot  = (*Manager).waitFirstBootDone
	stopForGrant   = (*Manager).Stop
	startForGrant  = (*Manager).Start
	waitSecondBoot = (*Manager).WaitReady
)

// ScreenSharingOutcome reports what the create-time grant achieved. It exists
// because "the pending flag is clear" is not the same as "Screen Sharing works":
// vee also clears the flag when it gives up on a guest whose privacy database it
// does not recognize, and telling that user their guest is authorized would be a
// lie they only discover at the first failed connection.
type ScreenSharingOutcome struct {
	// Granted is true when the guest's screen-sharing agent is authorized.
	Granted bool
	// Unsupported is true when vee gave up because the guest's privacy database
	// is not one it can write. Retrying will not help; the user has to enable
	// Screen Sharing inside the guest.
	Unsupported bool
	// GuestRunning reports whether the guest is running now. A create that
	// leaves it stopped has to say so.
	GuestRunning bool
}

// AuthorizeScreenSharing completes the boot cycle a macOS guest needs before its
// Screen Sharing service will serve a session.
//
// The grants have to be written into the guest's privacy database while its disk
// is idle, because SIP protects that database whenever the guest is running —
// but macOS only creates the database during the guest's *first* boot, so
// provisioning has nothing to write into. The guest therefore has to boot once,
// shut down, and start again, and the start is where vee writes the grants.
//
// Leaving that to the user means a freshly created guest silently refuses every
// Screen Sharing session until they happen to restart it, so `vee create` runs
// the cycle itself: wait for provisioning to finish inside the guest, stop it,
// start it again, and wait for it to come back up before claiming anything.
//
// The returned outcome describes what happened even when err is non-nil, so a
// caller can tell the user whether their guest is still running. progress, if
// non-nil, is called with each step for display.
func (m *Manager) AuthorizeScreenSharing(ctx context.Context, name string, progress func(string)) (ScreenSharingOutcome, error) {
	step := func(msg string) {
		if progress != nil {
			progress(msg)
		}
	}

	cfg, err := m.loadConfig(name)
	if err != nil {
		return ScreenSharingOutcome{}, err
	}
	if !screenSharingGrantOwed(cfg) {
		return m.screenSharingOutcome(name)
	}
	out := ScreenSharingOutcome{GuestRunning: true}

	step(fmt.Sprintf("waiting for the guest to finish provisioning (up to %s)", firstBootWait))
	if err := waitFirstBoot(m, ctx, cfg); err != nil {
		return out, err
	}

	// Past this point the cycle must finish. A context cancelled mid-sequence
	// would strip the graceful shutdown of the ssh call and the control-socket
	// request that implement it, turning `vee stop` into a 30-second wait and
	// then a SIGKILL — a power cut for a guest that just provisioned itself.
	if err := ctx.Err(); err != nil {
		return out, err
	}
	cycleCtx := context.WithoutCancel(ctx)

	// Stopping is safe now: provisioning has written its completion marker, so
	// nothing half-created is left behind.
	step("restarting the guest so its Screen Sharing permissions can be written")
	if err := stopForGrant(m, cycleCtx, name); err != nil {
		return out, fmt.Errorf("stop the guest: %w", err)
	}
	out.GuestRunning = false
	if err := startForGrant(m, cycleCtx, name, false); err != nil {
		return out, fmt.Errorf("%w: %w", ErrGuestLeftStopped, err)
	}
	out.GuestRunning = true

	// The guest is only a second into booting here, so nothing may be claimed
	// about Screen Sharing — or about `vee ssh` — until it is up again.
	step("waiting for the guest to come back up")
	waitErr := waitSecondBoot(m, cycleCtx, name, secondBootWait)

	after, err := m.screenSharingOutcome(name)
	if err != nil {
		return out, err
	}
	after.GuestRunning = true
	return after, waitErr
}

// screenSharingOutcome reads what the start path recorded. The persisted config
// is the authority: the grant runs inside Start, which may be a different
// process than the one asking.
func (m *Manager) screenSharingOutcome(name string) (ScreenSharingOutcome, error) {
	cfg, err := m.loadConfig(name)
	if err != nil {
		return ScreenSharingOutcome{}, err
	}
	unsupported := cfg.MacOS != nil && cfg.MacOS.ScreenSharingUnsupported
	return ScreenSharingOutcome{
		Granted:      !screenSharingGrantOwed(cfg) && !unsupported,
		Unsupported:  unsupported,
		GuestRunning: true,
	}, nil
}

// screenSharingGrantOwed reports whether vee still owes this VM its Screen
// Sharing grants.
func screenSharingGrantOwed(cfg *VMConfig) bool {
	return cfg.BackendName() == backend.VZ && cfg.MacOS != nil && cfg.MacOS.ScreenSharingGrantPending
}

// waitFirstBootDone waits for the marker the guest's provisioning script writes
// when it has finished. Stopping a guest mid-provisioning would leave it without
// the account or the SSH key it is being given.
func (m *Manager) waitFirstBootDone(ctx context.Context, cfg *VMConfig) error {
	if cfg.SSHUser == "" || cfg.NIC.MAC == "" {
		return fmt.Errorf("%w: no ssh user or MAC recorded", errFirstBootIncomplete)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return err
	}

	deadline := time.Now().Add(firstBootWait)
	// lastState describes the most recent answer, so a timeout can say what the
	// guest was actually doing rather than reporting a stale connection error.
	var lastState string
	for {
		if !m.guestStillRunning(cfg.Name) {
			return fmt.Errorf("%w — check %s", errGuestExited, vzhelper.LogPath(m.vmDir(cfg.Name)))
		}

		ip, ipErr := ResolveIPFromMAC(cfg.NIC.MAC)
		switch {
		case ipErr != nil:
			lastState = fmt.Sprintf("the guest had no DHCP lease yet: %v", ipErr)
		default:
			done, err := firstBootMarkerPresent(ctx, sshBin, cfg.SSHUser, ip, home)
			switch {
			case err != nil:
				lastState = fmt.Sprintf("the guest could not be reached: %v", err)
			case done:
				return nil
			default:
				lastState = "the guest was reachable but its provisioning script had not finished"
			}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%w within %s: %s", errFirstBootIncomplete, firstBootWait, lastState)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(firstBootPoll):
		}
	}
}

// guestStillRunning reports whether the VM's backend process is alive. Waiting
// six minutes for a guest that has already exited only delays the report.
func (m *Manager) guestStillRunning(name string) bool {
	state, err := m.loadState(name)
	if err != nil {
		// Unreadable state is not evidence the guest died; let the wait run.
		return true
	}
	return state.Running && isAlive(state.PID)
}

// firstBootMarkerPresent probes the guest for the provisioning marker. An
// unreachable guest is not an error worth reporting on its own — sshd comes up
// partway through the very boot being waited for.
func firstBootMarkerPresent(ctx context.Context, sshBin, user, ip, home string) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, firstBootProbeTimeout)
	defer cancel()

	// The remote command answers either way and exits zero, so "still
	// provisioning" cannot be mistaken for "cannot reach the guest".
	args := append(firstBootProbeArgs(user, ip, home),
		"if test -f "+firstBootMarkerPath+"; then echo "+firstBootMarkerOK+"; else echo "+firstBootMarkerPending+"; fi")
	//nolint:gosec // ssh from LookPath; arguments are vee-derived
	out, err := exec.CommandContext(probeCtx, sshBin, args...).CombinedOutput()
	switch {
	case strings.Contains(string(out), firstBootMarkerOK):
		return true, nil
	case strings.Contains(string(out), firstBootMarkerPending):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("probe guest: %w: %s", err, strings.TrimSpace(string(out)))
	default:
		return false, fmt.Errorf("probe guest: unrecognized answer: %s", strings.TrimSpace(string(out)))
	}
}

const (
	// firstBootMarkerPath is written by the provisioning script as its last act.
	firstBootMarkerPath = "/var/db/.vee-firstboot-done"
	// firstBootMarkerOK and firstBootMarkerPending keep the probe's answer
	// unambiguous: ssh mixes its own diagnostics into the same stream, and a
	// guest that is still provisioning must not read as a failed probe.
	firstBootMarkerOK      = "vee-firstboot-complete"
	firstBootMarkerPending = "vee-firstboot-pending"
)

// firstBootProbeArgs builds a non-interactive ssh invocation. It must never
// prompt: this runs inside `vee create`, where a hung prompt would strand a
// user who has already waited out a restore.
func firstBootProbeArgs(user, ip, home string) []string {
	return []string{
		"-i", filepath.Join(home, ".vee", "ssh", "id_ed25519"),
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=" + filepath.Join(home, ".vee", "ssh", "known_hosts"),
		"-o", "ConnectTimeout=5",
		"-o", "LogLevel=ERROR",
		user + "@" + ip,
	}
}
