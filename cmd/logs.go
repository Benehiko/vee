package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

var logsFollow bool

var logsCmd = &cobra.Command{
	Use:               "logs <name>",
	Short:             "Stream the QEMU log for a running or stopped VM",
	ValidArgsFunction: completeVMNames,
	Args:              cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		logPath := filepath.Join(prov.Config().StoragePath, name, "qemu.log")

		f, err := os.Open(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("no log found for VM %q (has it been started?)", name)
			}
			return err
		}
		defer func() { _ = f.Close() }()

		if _, err := io.Copy(os.Stdout, f); err != nil {
			return err
		}

		if !logsFollow {
			return nil
		}

		// Tail: poll for new data until the context is cancelled.
		for {
			select {
			case <-cmd.Context().Done():
				return nil
			case <-time.After(250 * time.Millisecond):
				if _, err := io.Copy(os.Stdout, f); err != nil {
					return err
				}
			}
		}
	},
}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Follow log output (like tail -f)")
}
