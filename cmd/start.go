package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Benehiko/vee/vm"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	startForeground bool
	startWaitReady  bool
)

var startCmd = &cobra.Command{
	Use:               "start <name>",
	Short:             "Start a VM",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		mgr := vm.NewManager(prov)
		stdinReader := bufio.NewReader(os.Stdin)
		mgr.PromptFn = func(prompt string) (string, error) {
			fmt.Fprint(os.Stderr, prompt)
			// Use hidden input for password prompts, plain readline for username.
			if strings.Contains(strings.ToLower(prompt), "password") {
				pw, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
				return string(pw), err
			}
			line, err := stdinReader.ReadString('\n')
			return strings.TrimRight(line, "\r\n"), err
		}
		if err := mgr.Start(cmd.Context(), name, startForeground); err != nil {
			return err
		}
		if startWaitReady {
			fmt.Printf("Waiting for VM %q to become ready (up to 10m)...\n", name)
			if err := mgr.WaitReady(cmd.Context(), name, 10*time.Minute); err != nil {
				return fmt.Errorf("wait-ready: %w", err)
			}
			fmt.Printf("VM %q is ready.\n", name)
		}
		return nil
	},
}

func init() {
	startCmd.Flags().BoolVar(&startForeground, "foreground", false, "Run in foreground (block until VM exits)")
	startCmd.Flags().BoolVar(&startWaitReady, "wait-ready", false, "Wait until SSH or guest-agent responds, then mark VM as ready")
}
