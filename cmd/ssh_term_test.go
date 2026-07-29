package cmd

import (
	"slices"
	"testing"
)

func TestKnownToMacOSTerminfo(t *testing.T) {
	// Measured against a real macOS 26 guest: infocmp knows the first group
	// and does not know the second.
	known := []string{"xterm", "xterm-256color", "screen-256color", "tmux-256color", "vt100", "ansi", "dumb"}
	unknown := []string{"xterm-ghostty", "xterm-kitty", "wezterm", "alacritty", "foot", "contour"}

	for _, term := range known {
		if !knownToMacOSTerminfo(term) {
			t.Errorf("knownToMacOSTerminfo(%q) = false, want true", term)
		}
	}
	for _, term := range unknown {
		if knownToMacOSTerminfo(term) {
			t.Errorf("knownToMacOSTerminfo(%q) = true, want false", term)
		}
	}
}

func TestSSHEnvOverridesUnknownTermForVZOnly(t *testing.T) {
	env := []string{"PATH=/usr/bin", "TERM=xterm-ghostty", "LANG=en_US.UTF-8"}

	// QEMU guests are left alone: their distros carry a broad terminfo, and
	// the user may have installed their own entry.
	if got := sshEnv(env, false); !slices.Equal(got, env) {
		t.Errorf("non-vz env was modified: %v", got)
	}

	got := sshEnv(env, true)
	if slices.Contains(got, "TERM=xterm-ghostty") {
		t.Error("unknown TERM survived for a vz guest")
	}
	if !slices.Contains(got, "TERM=xterm-256color") {
		t.Errorf("TERM was not replaced with a type the guest knows: %v", got)
	}
	for _, keep := range []string{"PATH=/usr/bin", "LANG=en_US.UTF-8"} {
		if !slices.Contains(got, keep) {
			t.Errorf("sshEnv dropped %q", keep)
		}
	}

	// A TERM the guest knows must pass through untouched.
	ok := []string{"PATH=/usr/bin", "TERM=xterm-256color"}
	if got := sshEnv(ok, true); !slices.Equal(got, ok) {
		t.Errorf("known TERM was rewritten: %v", got)
	}
}

func TestSSHEnvReadsTermFromItsArgument(t *testing.T) {
	// Regression test: sshEnv read TERM from the process environment, so it
	// silently did nothing wherever TERM is unset — including CI, which is
	// where it was caught.
	t.Setenv("TERM", "")
	got := sshEnv([]string{"TERM=xterm-ghostty"}, true)
	if !slices.Contains(got, "TERM=xterm-256color") {
		t.Errorf("TERM from the argument was ignored: %v", got)
	}

	// And with no TERM in the argument there is nothing to fix up.
	t.Setenv("TERM", "xterm-ghostty")
	if got := sshEnv([]string{"PATH=/usr/bin"}, true); !slices.Equal(got, []string{"PATH=/usr/bin"}) {
		t.Errorf("process TERM leaked into the result: %v", got)
	}
}
