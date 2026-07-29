package vm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"

	"github.com/Benehiko/vee/provider"
)

func TestScreenSharingGrantOwed(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *VMConfig
		want bool
	}{
		{
			name: "vz guest still owed its grants",
			cfg:  &VMConfig{Backend: "vz", MacOS: &MacOSConfig{ScreenSharingGrantPending: true}},
			want: true,
		},
		{
			name: "vz guest already granted",
			cfg:  &VMConfig{Backend: "vz", MacOS: &MacOSConfig{}},
			want: false,
		},
		{
			name: "vz guest vee never provisioned",
			cfg:  &VMConfig{Backend: "vz"},
			want: false,
		},
		{
			// Nothing about a QEMU VM should ever trigger a macOS restart, even
			// if a hand-edited config carries the flag.
			name: "qemu guest carrying the flag",
			cfg:  &VMConfig{MacOS: &MacOSConfig{ScreenSharingGrantPending: true}},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := screenSharingGrantOwed(tc.cfg); got != tc.want {
				t.Errorf("screenSharingGrantOwed = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAuthorizeScreenSharingSkipsGuestsThatAreNotOwed(t *testing.T) {
	// A guest with nothing pending must not be restarted: `vee create` would be
	// bouncing a working VM for no reason. Reaching the restart at all would
	// fail this test, since the fake manager cannot stop anything.
	m := &Manager{provider: grantProvider{
		cfg:     &provider.Config{StoragePath: t.TempDir()},
		entries: &[]zapcore.Entry{},
	}}
	cfg := &VMConfig{Name: "mac", Backend: "vz", MacOS: &MacOSConfig{}}
	if err := m.saveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	out, err := m.AuthorizeScreenSharing(t.Context(), "mac", nil)
	if err != nil {
		t.Fatalf("AuthorizeScreenSharing: %v", err)
	}
	if !out.Granted {
		t.Error("a guest with nothing pending reported as not granted")
	}
}

func TestWaitFirstBootDoneNeedsAReachableGuest(t *testing.T) {
	// Without an ssh user or a MAC there is no way to ask the guest whether it
	// has finished, and stopping it blind could interrupt provisioning.
	m := &Manager{}
	for _, tc := range []struct {
		name string
		cfg  *VMConfig
	}{
		{"no ssh user", &VMConfig{NIC: NICConfig{MAC: "52:54:00:00:00:01"}}},
		{"no MAC", &VMConfig{SSHUser: "vee"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := m.waitFirstBootDone(t.Context(), tc.cfg)
			if !errors.Is(err, errFirstBootIncomplete) {
				t.Errorf("waitFirstBootDone = %v, want errFirstBootIncomplete", err)
			}
		})
	}
}

// fakeSSH writes a script that stands in for ssh, printing what a real probe
// would have received from the guest.
func fakeSSH(t *testing.T, stdout string, exitCode int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh")
	script := "#!/bin/sh\nprintf '%s\\n' " + shellQuote(stdout) + "\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	return path
}

func TestFirstBootMarkerPresent(t *testing.T) {
	// The three answers a probe can get must stay distinguishable: a guest that
	// is still provisioning must not be reported as a failure, or the wait would
	// spend its whole budget on a healthy guest and then give up.
	for _, tc := range []struct {
		name     string
		stdout   string
		exitCode int
		wantDone bool
		wantErr  bool
	}{
		{name: "provisioning finished", stdout: firstBootMarkerOK, wantDone: true},
		{name: "still provisioning", stdout: firstBootMarkerPending},
		{
			name:     "guest not reachable yet",
			stdout:   "ssh: connect to host 192.0.2.1 port 22: Connection refused",
			exitCode: 5,
			wantErr:  true,
		},
		{
			name:    "answer we do not recognize",
			stdout:  "something else entirely",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done, err := firstBootMarkerPresent(t.Context(),
				fakeSSH(t, tc.stdout, tc.exitCode), "vee", "192.0.2.1", t.TempDir())
			if done != tc.wantDone {
				t.Errorf("done = %v, want %v", done, tc.wantDone)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestFirstBootProbeNeverPrompts(t *testing.T) {
	// This runs inside `vee create`. An ssh that stops to ask for a password or
	// to confirm a host key would hang a create that has already spent tens of
	// minutes on a restore.
	args := strings.Join(firstBootProbeArgs("vee", "192.0.2.1", "/home/u"), " ")
	for _, want := range []string{"BatchMode=yes", "StrictHostKeyChecking=no", "ConnectTimeout=5"} {
		if !strings.Contains(args, want) {
			t.Errorf("probe args missing %s: %s", want, args)
		}
	}
	if !strings.Contains(args, filepath.Join("/home/u", ".vee", "ssh", "id_ed25519")) {
		t.Errorf("probe does not use vee's key: %s", args)
	}
}

// cycleHarness stubs the four steps of the create-time cycle and records the
// order they ran in — the order is the behaviour worth protecting.
type cycleHarness struct {
	m     *Manager
	name  string
	steps []string
	// waitErr, stopErr, startErr and readyErr are returned by their step.
	waitErr, stopErr, startErr, readyErr error
	// onStart simulates what the real start does to the persisted config: it is
	// the start that writes the grants and records the result.
	onStart func(cfg *VMConfig)
}

func newCycleHarness(t *testing.T, pending bool) *cycleHarness {
	t.Helper()
	m := &Manager{provider: grantProvider{
		cfg:     &provider.Config{StoragePath: t.TempDir()},
		entries: &[]zapcore.Entry{},
	}}
	cfg := &VMConfig{
		Name:    "mac",
		Backend: "vz",
		SSHUser: "vee",
		NIC:     NICConfig{MAC: "52:54:00:00:00:01"},
		MacOS:   &MacOSConfig{ScreenSharingGrantPending: pending},
	}
	if err := m.saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	h := &cycleHarness{m: m, name: cfg.Name}

	origWait, origStop, origStart, origReady := waitFirstBoot, stopForGrant, startForGrant, waitSecondBoot
	t.Cleanup(func() {
		waitFirstBoot, stopForGrant, startForGrant, waitSecondBoot = origWait, origStop, origStart, origReady
	})

	waitFirstBoot = func(*Manager, context.Context, *VMConfig) error {
		h.steps = append(h.steps, "wait-first-boot")
		return h.waitErr
	}
	stopForGrant = func(*Manager, context.Context, string) error {
		h.steps = append(h.steps, "stop")
		return h.stopErr
	}
	startForGrant = func(m *Manager, _ context.Context, name string, _ bool) error {
		h.steps = append(h.steps, "start")
		if h.startErr != nil {
			return h.startErr
		}
		if h.onStart != nil {
			cfg, err := m.loadConfig(name)
			if err != nil {
				return err
			}
			h.onStart(cfg)
			return m.saveConfig(cfg)
		}
		return nil
	}
	waitSecondBoot = func(*Manager, context.Context, string, time.Duration) error {
		h.steps = append(h.steps, "wait-second-boot")
		return h.readyErr
	}
	return h
}

// grantsApplied is what a successful start records.
func grantsApplied(cfg *VMConfig) { cfg.MacOS.ScreenSharingGrantPending = false }

func TestAuthorizeScreenSharingRunsTheWholeCycleInOrder(t *testing.T) {
	h := newCycleHarness(t, true)
	h.onStart = grantsApplied

	out, err := h.m.AuthorizeScreenSharing(t.Context(), h.name, nil)
	if err != nil {
		t.Fatalf("AuthorizeScreenSharing: %v", err)
	}
	want := []string{"wait-first-boot", "stop", "start", "wait-second-boot"}
	if strings.Join(h.steps, ",") != strings.Join(want, ",") {
		t.Errorf("steps = %v, want %v", h.steps, want)
	}
	if !out.Granted || !out.GuestRunning {
		t.Errorf("outcome = %+v, want granted and running", out)
	}
}

func TestAuthorizeScreenSharingWaitsForProvisioningBeforeStopping(t *testing.T) {
	// Stopping a guest mid-provisioning would leave it without the account or
	// the key it is being given, so a failed wait must stop the sequence dead.
	h := newCycleHarness(t, true)
	h.waitErr = errFirstBootIncomplete

	out, err := h.m.AuthorizeScreenSharing(t.Context(), h.name, nil)
	if !errors.Is(err, errFirstBootIncomplete) {
		t.Fatalf("err = %v, want errFirstBootIncomplete", err)
	}
	if len(h.steps) != 1 || h.steps[0] != "wait-first-boot" {
		t.Errorf("steps = %v, want the sequence to stop after the wait", h.steps)
	}
	if !out.GuestRunning {
		t.Error("the guest was never stopped, so it must still be reported as running")
	}
}

func TestAuthorizeScreenSharingReportsAGuestItLeftStopped(t *testing.T) {
	// The worst outcome to misreport: the user asked for a running VM and the
	// restart failed after the stop succeeded.
	h := newCycleHarness(t, true)
	h.startErr = errors.New("helper exited during startup")

	out, err := h.m.AuthorizeScreenSharing(t.Context(), h.name, nil)
	if !errors.Is(err, ErrGuestLeftStopped) {
		t.Fatalf("err = %v, want ErrGuestLeftStopped", err)
	}
	if out.GuestRunning {
		t.Error("reported the guest as running after the restart failed")
	}
	if out.Granted {
		t.Error("reported the grants as applied when the guest never started")
	}
}

func TestAuthorizeScreenSharingDoesNotCallAGivenUpGuestAuthorized(t *testing.T) {
	// The start path clears the pending flag when it gives up on a privacy
	// database it does not recognize. Reading only that flag would tell the user
	// Screen Sharing works, which they would discover is false at the first
	// connection attempt.
	h := newCycleHarness(t, true)
	h.onStart = func(cfg *VMConfig) {
		cfg.MacOS.ScreenSharingGrantPending = false
		cfg.MacOS.ScreenSharingUnsupported = true
	}

	out, err := h.m.AuthorizeScreenSharing(t.Context(), h.name, nil)
	if err != nil {
		t.Fatalf("AuthorizeScreenSharing: %v", err)
	}
	if out.Granted {
		t.Error("a guest vee gave up on was reported as authorized")
	}
	if !out.Unsupported {
		t.Error("the give-up reason was not reported, so the user gets no explanation")
	}
}

func TestAuthorizeScreenSharingSurfacesASlowSecondBoot(t *testing.T) {
	// The grants are written by the start, so they are in place even if the
	// guest is slow to come back — but create must not claim `vee view` works.
	h := newCycleHarness(t, true)
	h.onStart = grantsApplied
	h.readyErr = errors.New("did not acquire a DHCP lease within 10m")

	out, err := h.m.AuthorizeScreenSharing(t.Context(), h.name, nil)
	if err == nil {
		t.Fatal("a guest that never came back must be reported")
	}
	if !out.GuestRunning {
		t.Error("the guest was started, so it must be reported as running")
	}
	if !out.Granted {
		t.Error("the grants were written by the start; the outcome should say so")
	}
}

func TestAuthorizeScreenSharingStopsIfTheCreateWasCancelled(t *testing.T) {
	// A cancelled context strips the graceful shutdown of the ssh call that
	// implements it, so stopping anyway would power-cut the guest.
	h := newCycleHarness(t, true)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := h.m.AuthorizeScreenSharing(ctx, h.name, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	for _, s := range h.steps {
		if s == "stop" {
			t.Error("stopped the guest with a cancelled context, which turns a clean shutdown into a kill")
		}
	}
}
