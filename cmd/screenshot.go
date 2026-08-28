package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/vm"
)

var screenshotCmd = &cobra.Command{
	Use:   "screenshot <name> [output.png]",
	Short: "Capture a running VM's display to a PNG file",
	Long: `Capture a running VM's display and write it to a PNG file.

The capture goes through QMP's screendump (QEMU backend only), so it works
headless — nothing needs to be attached to the VM's display. QEMU dumps a
PPM image; vee converts it to PNG.

Without an output path the file lands in the current directory as
<name>-<timestamp>.png. The absolute path of the written file is the only
thing printed on stdout, so it stays scriptable:

  open "$(vee screenshot myvm)"

vz-backed VMs have no QMP socket; connect to a macOS guest's Screen Sharing
via "vee view" instead.

Examples:
  # Timestamped PNG in the current directory
  vee screenshot myvm

  # Explicit destination
  vee screenshot myvm /tmp/desktop.png`,
	Args:              cobra.RangeArgs(1, 2),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		out := fmt.Sprintf("%s-%s.png", name, time.Now().Format("20060102-150405"))
		if len(args) == 2 {
			out = args[1]
		}

		data, width, height, warning, err := vm.NewManager(prov).Screenshot(cmd.Context(), name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, data, 0o600); err != nil {
			return err
		}

		abs, err := filepath.Abs(out)
		if err != nil {
			abs = out
		}
		cmd.PrintErrf("captured %dx%d\n", width, height)
		if warning != "" {
			cmd.PrintErrf("warning: %s\n", warning)
		}
		fmt.Println(abs)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(screenshotCmd)
}
