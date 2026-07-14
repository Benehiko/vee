//go:build windows

package cmd

import (
	"os"
	"os/exec"
)

// execSSH spawns ssh as a child process and waits for it, forwarding stdio.
// Windows has no execve(2) equivalent (syscall.Exec is unimplemented), so vee
// remains as the parent for the lifetime of the SSH session. The child's exit
// error (including a non-zero exit code) is returned to the caller.
func execSSH(sshBin string, sshArgs, env []string) error {
	//nolint:gosec // sshBin resolved via LookPath; args built from vetted VM config for an interactive SSH session.
	cmd := exec.Command(sshBin, sshArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env
	return cmd.Run()
}
