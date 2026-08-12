package vm

import (
	"slices"
	"strings"
	"testing"
)

func TestVZShutdownArgs(t *testing.T) {
	args := vzShutdownArgs("vee", "192.168.64.9", "/Users/someone")

	// The command must be able to run unattended: a prompt would hang `vee
	// stop` until its grace period expired, which is the bug this fixes.
	for _, required := range []string{"BatchMode=yes", "ConnectTimeout=5"} {
		if !slices.Contains(args, required) {
			t.Errorf("missing %q in %v", required, args)
		}
	}
	if !slices.Contains(args, "vee@192.168.64.9") {
		t.Errorf("destination missing from %v", args)
	}

	// sudo -n so a guest without the sudoers rule fails fast instead of
	// waiting for a password nobody can type.
	last := args[len(args)-1]
	if !strings.Contains(last, "sudo -n /sbin/shutdown -h now") {
		t.Errorf("remote command = %q, want a non-interactive shutdown", last)
	}

	// The vee-managed key and known_hosts, not the user's personal ones.
	joined := strings.Join(args, " ")
	for _, want := range []string{"/Users/someone/.vee/ssh/id_ed25519", "/Users/someone/.vee/ssh/known_hosts"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
}

func TestVZVsockOpError(t *testing.T) {
	// A helper that predates the vsock ops answers with "unknown op"; the
	// user needs to hear "update the helper", not a raw protocol error.
	err := vzVsockOpError(`unknown op "vsock-connect"`)
	for _, want := range []string{"vee-vz-helper", "make vz-helper", "restart the VM"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("unknown-op error %q is missing %q", err, want)
		}
	}

	// Any other helper error must pass through untouched.
	if got := vzVsockOpError("vsock is not enabled in the machine spec").Error(); got != "vsock is not enabled in the machine spec" {
		t.Errorf("passthrough error = %q", got)
	}
}
