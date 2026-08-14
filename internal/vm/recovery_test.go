package vm

import (
	"strings"
	"testing"
)

// RecoveryPlan is the single dispatch point for `vee start --recovery`
// (issue #134): launch-settable recovery exists only on the vz backend
// (macOS start option, direct-kernel Linux cmdline); everything else is a
// warn-and-boot-normally no-op whose message tells the user the real path.
func TestRecoveryPlan(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *VMConfig
		want     RecoveryMode
		contains string
	}{
		{
			name:     "vz macos guest",
			cfg:      &VMConfig{Name: "mac", Backend: "vz", MacOS: &MacOSConfig{}},
			want:     RecoveryMacOS,
			contains: "vee view mac",
		},
		{
			name:     "vz direct-kernel linux guest",
			cfg:      &VMConfig{Name: "lin", Backend: "vz", Kernel: "/kernels/vmlinux"},
			want:     RecoveryLinuxKernel,
			contains: linuxRescueCmdline,
		},
		{
			name:     "vz EFI linux guest has no cmdline hook",
			cfg:      &VMConfig{Name: "lin", Backend: "vz"},
			want:     RecoveryUnsupported,
			contains: "booting normally",
		},
		{
			name:     "windows guest arms WinRE from inside",
			cfg:      &VMConfig{Name: "win", Backend: "qemu", Template: "windows"},
			want:     RecoveryUnsupported,
			contains: "reagentc /boottore",
		},
		{
			name: "qemu whole-disk guest has no cmdline hook",
			// The empty backend resolves to QEMU (configs written before the
			// backend field existed).
			cfg:      &VMConfig{Name: "srv", Template: "docker"},
			want:     RecoveryUnsupported,
			contains: "booting normally",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, msg := RecoveryPlan(tt.cfg)
			if mode != tt.want {
				t.Errorf("mode = %d, want %d", mode, tt.want)
			}
			if !strings.Contains(msg, tt.contains) {
				t.Errorf("message %q does not mention %q", msg, tt.contains)
			}
			if msg == "" {
				t.Error("every plan must carry user guidance — silence is the failure mode the issue forbids")
			}
		})
	}
}
