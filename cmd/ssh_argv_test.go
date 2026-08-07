package cmd

import (
	"slices"
	"strings"
	"testing"
)

func TestParseSSHArgv(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		wantName string
		wantUser string
		wantID   string
		wantFlag []string
		wantCmd  []string
		wantErr  string
	}{
		{
			name:     "bare name",
			argv:     []string{"myvm"},
			wantName: "myvm",
		},
		{
			name:     "ssh flag with separate value before name",
			argv:     []string{"-L", "8080:localhost:8080", "myvm"},
			wantName: "myvm",
			wantFlag: []string{"-L", "8080:localhost:8080"},
		},
		{
			name:     "ssh flag with attached value",
			argv:     []string{"-L8080:localhost:8080", "myvm"},
			wantName: "myvm",
			wantFlag: []string{"-L", "8080:localhost:8080"},
		},
		{
			name:     "clustered boolean flags",
			argv:     []string{"-NT", "myvm"},
			wantName: "myvm",
			wantFlag: []string{"-N", "-T"},
		},
		{
			name:     "repeated verbosity",
			argv:     []string{"-vvv", "myvm"},
			wantName: "myvm",
			wantFlag: []string{"-v", "-v", "-v"},
		},
		{
			name:     "cluster ending in a value flag",
			argv:     []string{"-Ao", "StrictHostKeyChecking=no", "myvm"},
			wantName: "myvm",
			wantFlag: []string{"-A", "-o", "StrictHostKeyChecking=no"},
		},
		{
			name:     "ssh -l maps to user",
			argv:     []string{"-l", "root", "myvm"},
			wantName: "myvm",
			wantUser: "root",
		},
		{
			name:     "ssh -i maps to identity",
			argv:     []string{"-i", "/k/id_ed25519", "myvm"},
			wantName: "myvm",
			wantID:   "/k/id_ed25519",
		},
		{
			name:     "vee long flags",
			argv:     []string{"--user", "root", "--identity=/k/id", "--ssh-flag=-A", "myvm"},
			wantName: "myvm",
			wantUser: "root",
			wantID:   "/k/id",
			wantFlag: []string{"-A"},
		},
		{
			name:     "flags may follow the name",
			argv:     []string{"myvm", "-A"},
			wantName: "myvm",
			wantFlag: []string{"-A"},
		},
		{
			name:     "remote command after dash dash",
			argv:     []string{"myvm", "--", "sh", "-c", "echo hi"},
			wantName: "myvm",
			wantCmd:  []string{"sh", "-c", "echo hi"},
		},
		{
			name:     "flags in the remote command are not parsed",
			argv:     []string{"-L", "1:2:3", "myvm", "--", "ls", "-l", "--color=auto"},
			wantName: "myvm",
			wantFlag: []string{"-L", "1:2:3"},
			wantCmd:  []string{"ls", "-l", "--color=auto"},
		},
		{
			name:     "empty remote command",
			argv:     []string{"myvm", "--"},
			wantName: "myvm",
		},
		{
			name:    "missing name",
			argv:    []string{"-A"},
			wantErr: "VM name is required",
		},
		{
			name:    "second positional needs dash dash",
			argv:    []string{"myvm", "uptime"},
			wantErr: "put the remote command after --",
		},
		{
			name:    "value flag at end of argv",
			argv:    []string{"myvm", "-L"},
			wantErr: "needs an argument",
		},
		{
			name:    "unknown short flag",
			argv:    []string{"-Z", "myvm"},
			wantErr: "unknown ssh flag -Z",
		},
		{
			name:    "unknown long flag",
			argv:    []string{"--nope", "myvm"},
			wantErr: "unknown flag --nope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSSHArgv(tt.argv)
			if tt.wantErr != "" {
				if err == nil {
					return
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tt.wantName)
			}
			if got.User != tt.wantUser {
				t.Errorf("User = %q, want %q", got.User, tt.wantUser)
			}
			if got.Identity != tt.wantID {
				t.Errorf("Identity = %q, want %q", got.Identity, tt.wantID)
			}
			if !slices.Equal(got.SSHFlags, tt.wantFlag) {
				t.Errorf("SSHFlags = %q, want %q", got.SSHFlags, tt.wantFlag)
			}
			if !slices.Equal(got.Command, tt.wantCmd) {
				t.Errorf("Command = %q, want %q", got.Command, tt.wantCmd)
			}
		})
	}
}

// TestParseSSHArgvErrors asserts the cases above that must fail actually do —
// TestParseSSHArgv tolerates a nil error to keep its table readable.
func TestParseSSHArgvErrors(t *testing.T) {
	for _, argv := range [][]string{
		{"-A"},
		{"myvm", "uptime"},
		{"myvm", "-L"},
		{"-Z", "myvm"},
		{"--nope", "myvm"},
		{"--user"},
	} {
		if _, err := parseSSHArgv(argv); err == nil {
			t.Errorf("parseSSHArgv(%q) succeeded, want an error", argv)
		}
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "''"},
		{"plain", "plain"},
		{"/usr/bin/env", "/usr/bin/env"},
		{"a=b,c.d-e_f@g%h:i", "a=b,c.d-e_f@g%h:i"},
		{"a b", "'a b'"},
		{"x  y", "'x  y'"},
		{`say "hi"`, `'say "hi"'`},
		{"it's", `'it'\''s'`},
		{"$HOME", `'$HOME'`},
		{"a;b", "'a;b'"},
		{"`id`", "'`id`'"},
		{"*", "'*'"},
		{"new\nline", "'new\nline'"},
	}
	for _, tt := range tests {
		if got := shellQuote(tt.in); got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestBuildSSHArgsRemoteCommand covers the round trip that issue #100
// describes: ssh joins its remote-command arguments with spaces and lets the
// remote shell re-split them, so vee has to quote each one.
func TestBuildSSHArgsRemoteCommand(t *testing.T) {
	tests := []struct {
		name      string
		remoteCmd []string
		want      string
	}{
		{
			name:      "multi word argument stays one argument",
			remoteCmd: []string{"echo", "a b"},
			want:      "echo 'a b'",
		},
		{
			name:      "sh -c keeps its whole script as one argument",
			remoteCmd: []string{"sh", "-c", `echo "x  y"; echo done`},
			want:      `sh -c 'echo "x  y"; echo done'`,
		},
		{
			name:      "local expansion is not performed remotely either",
			remoteCmd: []string{"echo", "$HOME"},
			want:      `echo '$HOME'`,
		},
		{
			name:      "nested single quotes survive",
			remoteCmd: []string{"bash", "-c", `cd /x && echo "it's fine"`},
			want:      `bash -c 'cd /x && echo "it'\''s fine"'`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildSSHArgs("vee", "10.0.0.5", 22, "", "", tt.remoteCmd, nil)
			if len(args) == 0 {
				t.Fatal("buildSSHArgs returned no args")
			}
			got := args[len(args)-1]
			if got != tt.want {
				t.Errorf("remote command arg = %q, want %q", got, tt.want)
			}
			// The remote command must be a single ssh argument, immediately
			// after the destination.
			if dest := args[len(args)-2]; dest != "vee@10.0.0.5" {
				t.Errorf("arg before the remote command = %q, want the destination", dest)
			}
		})
	}
}

// TestBuildSSHArgsUserFlagsPrecedeDefaults pins the ordering that lets a user
// -o override vee's: ssh honours the first value it sees for an option.
func TestBuildSSHArgsUserFlagsPrecedeDefaults(t *testing.T) {
	args := buildSSHArgs("vee", "10.0.0.5", 22, "", "/tmp/known_hosts",
		nil, []string{"-o", "StrictHostKeyChecking=no"})

	userIdx := slices.Index(args, "StrictHostKeyChecking=no")
	veeIdx := slices.Index(args, "StrictHostKeyChecking=accept-new")
	if userIdx < 0 || veeIdx < 0 {
		t.Fatalf("expected both -o values in %q", args)
	}
	if userIdx > veeIdx {
		t.Errorf("user -o at %d comes after vee's at %d; ssh would ignore it (args: %q)", userIdx, veeIdx, args)
	}
}

// TestBuildSSHArgsNoRemoteCommand keeps interactive sessions free of a trailing
// empty argument, which ssh would treat as a (blank) remote command and so
// never allocate a pty for.
func TestBuildSSHArgsNoRemoteCommand(t *testing.T) {
	args := buildSSHArgs("vee", "10.0.0.5", 2222, "", "", nil, nil)
	if got := args[len(args)-1]; got != "vee@10.0.0.5" {
		t.Errorf("last arg = %q, want the destination with nothing after it", got)
	}
	if !slices.Contains(args, "-p") {
		t.Errorf("expected -p for the non-default port in %q", args)
	}
}
