package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	veessh "github.com/Benehiko/vee/internal/ssh"
	"github.com/Benehiko/vee/internal/vm"
)

var sshShareAgentSock string

var sshShareCmd = &cobra.Command{
	Use:               "ssh-share <name>",
	Short:             "Share host SSH agent into a running VM via AF_VSOCK",
	ValidArgsFunction: completeVMNames,
	Long: `Starts a vsock proxy on port 2222 that forwards connections from the guest
to the host SSH agent socket. The VM must have been created with --ssh-share
and be currently running.

Inside the guest, configure the SSH agent socket with socat or the vee
cloud-init RunCmd (installed automatically on devbox/server templates):

  export SSH_AUTH_SOCK=/run/vee/ssh_agent.sock
  ssh-add -l`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		mgr := vm.NewManager(prov)
		entries, err := mgr.List()
		if err != nil {
			return err
		}

		var found bool
		for _, e := range entries {
			if e.Config.Name == name {
				found = true
				if !e.State.Running {
					return fmt.Errorf("VM %q is not running", name)
				}
				if !e.Config.SSHShare {
					return fmt.Errorf("VM %q was not created with --ssh-share; recreate it with that flag", name)
				}
				break
			}
		}
		if !found {
			return fmt.Errorf("VM %q not found", name)
		}

		proxy, err := veessh.NewProxy(sshShareAgentSock)
		if err != nil {
			return fmt.Errorf("create vsock proxy: %w", err)
		}
		if err := proxy.Listen(); err != nil {
			return fmt.Errorf("listen vsock: %w", err)
		}

		agentSock := sshShareAgentSock
		if agentSock == "" {
			agentSock = os.Getenv("SSH_AUTH_SOCK")
		}
		fmt.Printf("Sharing SSH agent (%s) into VM %q via vsock port 2222\n", agentSock, name)
		fmt.Printf("Press Ctrl-C to stop.\n")

		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

		errCh := make(chan error, 1)
		go func() { errCh <- proxy.Serve() }()

		select {
		case <-stop:
			_ = proxy.Close()
			return nil
		case err := <-errCh:
			return err
		}
	},
}

func init() {
	sshShareCmd.Flags().StringVar(&sshShareAgentSock, "agent-sock", "", "SSH agent socket path (default: $SSH_AUTH_SOCK)")
}
