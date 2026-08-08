package cmd

import (
	"testing"

	"github.com/spf13/pflag"
)

// TestNICBridgeDefaultReachesOpts covers --nic-mode=bridge given without an
// explicit --nic-bridge. The Changed() guards in optsFromFlags mean an unset
// flag never reaches opts, so the "br0" default used to be dropped and QEMU
// received "br=", failing with "access denied by acl file" — a message that
// points at permissions rather than at the missing interface name.
func TestNICBridgeDefaultReachesOpts(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "bridge mode without explicit bridge uses the default",
			args: []string{"dl", "--template", "torrent", "--nic-mode", "bridge"},
			want: "br0",
		},
		{
			name: "explicit bridge is honoured",
			args: []string{"dl", "--template", "torrent", "--nic-mode", "bridge", "--nic-bridge", "br1"},
			want: "br1",
		},
		{
			name: "user mode leaves the bridge unset",
			args: []string{"dl", "--template", "torrent", "--nic-mode", "user"},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The flag variables are package-level, so reset them between
			// runs to keep cases independent.
			createNicMode, createNicBridge = "user", "br0"
			t.Cleanup(func() { createNicMode, createNicBridge = "user", "br0" })

			// createCmd's flags are registered in init(); parsing mutates its
			// Changed() state, so restore it after each case.
			cmd := createCmd
			t.Cleanup(func() {
				cmd.Flags().Visit(func(f *pflag.Flag) { f.Changed = false })
			})
			if err := cmd.ParseFlags(tc.args); err != nil {
				t.Fatalf("ParseFlags(%v): %v", tc.args, err)
			}

			opts := optsFromFlags(cmd, "dl")
			if opts.NICBridge != tc.want {
				t.Errorf("NICBridge = %q, want %q", opts.NICBridge, tc.want)
			}
		})
	}
}
