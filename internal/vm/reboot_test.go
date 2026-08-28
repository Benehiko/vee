package vm

import (
	"encoding/json"
	"testing"

	"github.com/Benehiko/vee/internal/qemu"
)

func TestDesiredStateForReason(t *testing.T) {
	tests := map[string]string{
		ShutdownReasonUser:   DesiredStateStopped,
		ShutdownReasonGuest:  DesiredStateStopped,
		ShutdownReasonCrash:  DesiredStateStopped,
		ShutdownReasonHost:   DesiredStateRunning,
		ShutdownReasonReboot: DesiredStateRunning,
	}
	for reason, want := range tests {
		if got := desiredStateForReason(reason); got != want {
			t.Errorf("desiredStateForReason(%q) = %q, want %q", reason, got, want)
		}
	}
}

func TestShouldDaemonStartAfterReboot(t *testing.T) {
	// A failed reboot relaunch leaves reason=reboot with DesiredState=running;
	// the autostart pass must pick it back up (unlike a guest poweroff).
	state := &VMState{DesiredState: DesiredStateRunning, LastShutdownReason: ShutdownReasonReboot}
	if !shouldDaemonStart(state) {
		t.Error("daemon must restart a VM parked by a failed reboot power-cycle")
	}
	guest := &VMState{DesiredState: DesiredStateStopped, LastShutdownReason: ShutdownReasonGuest}
	if shouldDaemonStart(guest) {
		t.Error("daemon must not restart a guest-poweroff VM")
	}
}

func TestShouldPowerCycleOnGuestReset(t *testing.T) {
	m := newTestManager(t)
	if err := m.SaveConfig(&VMConfig{Name: "resetvm", Template: "desktop"}); err != nil {
		t.Fatal(err)
	}

	// Installer-driven reboots (a pending install pass) stay in place.
	if err := m.SaveState("resetvm", &VMState{Running: true, InstallState: InstallStatePending}); err != nil {
		t.Fatal(err)
	}
	if m.shouldPowerCycleOnGuestReset("resetvm") {
		t.Error("must not power-cycle during a pending install pass")
	}

	if err := m.SaveState("resetvm", &VMState{Running: true, InstallState: InstallStateReady}); err != nil {
		t.Fatal(err)
	}
	if !m.shouldPowerCycleOnGuestReset("resetvm") {
		t.Error("must power-cycle an installed guest that requested a reset")
	}

	if m.shouldPowerCycleOnGuestReset("no-such-vm") {
		t.Error("must not power-cycle a VM without state")
	}
}

func TestHandleResetEventIgnoresHostReset(t *testing.T) {
	// `vee qmp system_reset` (guest=false) keeps QEMU's in-place warm reset;
	// only guest-requested resets trigger the power-cycle.
	m := newTestManager(t)
	data, _ := json.Marshal(qemu.ResetEventData{Guest: false, Reason: "host-qmp-system-reset"})
	ev := qemu.QMPEvent{Event: "RESET", Data: data}
	if m.handleResetEvent(t.Context(), "anyvm", ev) {
		t.Error("host-initiated RESET must not stop the watcher or power-cycle")
	}
}
