//go:build !windows

package cmd

import "syscall"

// execSSH replaces the current process with ssh via execve(2) so that terminal
// signals (Ctrl-C, window resize) flow directly to ssh and vee leaves no
// lingering parent process. This call does not return on success.
func execSSH(sshBin string, sshArgs, env []string) error {
	//nolint:gosec // sshBin resolved via LookPath; args built from vetted VM config for an interactive SSH session.
	return syscall.Exec(sshBin, append([]string{"ssh"}, sshArgs...), env)
}
