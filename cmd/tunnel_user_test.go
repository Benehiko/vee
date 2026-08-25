package cmd

import (
	"testing"

	"github.com/Benehiko/vee/internal/vm"
)

// TestTunnelSSHUser covers the account vee tunnel logs in as.
//
// The regression this guards is silent and misleading: templates that create
// no user of their own (docker, dns-sink, bitmagnet — the Alpine image ships
// no bash for useradd) leave CloudInit.User empty and carry the SSH keys on
// the image's default account. Reading CloudInit.User directly yielded "", so
// ssh(1) substituted the host username and the tunnel failed with "Permission
// denied (publickey)" against an account that never existed in the guest —
// while vee ssh to the same VM worked, because it resolves the user properly.
func TestTunnelSSHUser(t *testing.T) {
	cases := []struct {
		name string
		cfg  *vm.VMConfig
		want string
	}{
		{
			// The bug: keys live on the image's default account.
			name: "alpine template with only a default user",
			cfg: &vm.VMConfig{
				Template:  "bitmagnet",
				CloudInit: &vm.CloudInitConfig{DefaultUser: "alpine"},
			},
			want: "alpine",
		},
		{
			name: "template that creates its own user",
			cfg: &vm.VMConfig{
				Template:  "torrent",
				CloudInit: &vm.CloudInitConfig{User: "vee", DefaultUser: "ubuntu"},
			},
			want: "vee",
		},
		{
			name: "explicit ssh_user override wins",
			cfg: &vm.VMConfig{
				SSHUser:   "operator",
				CloudInit: &vm.CloudInitConfig{User: "vee", DefaultUser: "ubuntu"},
			},
			want: "operator",
		},
		{
			name: "truenas stores its admin account outside cloud-init",
			cfg:  &vm.VMConfig{Template: "truenas", TrueNASUser: "admin"},
			want: "admin",
		},
		{
			// macOS guests carry no cloud-init account; ssh's default applies.
			name: "no account at all",
			cfg:  &vm.VMConfig{Template: "macos"},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tunnelSSHUser(tc.cfg); got != tc.want {
				t.Errorf("tunnelSSHUser() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTunnelSSHUserMatchesSSHCommand pins the two surfaces together. vee ssh
// and vee tunnel reaching the same guest as different accounts is the bug
// class this whole fix is about, so they must agree for every config shape.
func TestTunnelSSHUserMatchesSSHCommand(t *testing.T) {
	configs := []*vm.VMConfig{
		{Template: "bitmagnet", CloudInit: &vm.CloudInitConfig{DefaultUser: "alpine"}},
		{Template: "dns-sink", CloudInit: &vm.CloudInitConfig{DefaultUser: "alpine"}},
		{Template: "torrent", CloudInit: &vm.CloudInitConfig{User: "vee", DefaultUser: "ubuntu"}},
		{SSHUser: "operator", CloudInit: &vm.CloudInitConfig{User: "vee"}},
	}
	for _, cfg := range configs {
		// vee ssh resolves the user as cfg.SSHUsername(), falling back to the
		// TrueNAS admin account; tunnelSSHUser must produce the same answer.
		want := cfg.SSHUsername()
		if want == "" && cfg.Template == "truenas" {
			want = cfg.TrueNASUser
		}
		if got := tunnelSSHUser(cfg); got != want {
			t.Errorf("template %q: tunnel uses %q but vee ssh uses %q", cfg.Template, got, want)
		}
	}
}
