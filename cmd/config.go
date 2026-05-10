package cmd

import (
	"github.com/Benehiko/vee/internal/tui"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config [name]",
	Short: "Edit an existing VM's configuration in an interactive TUI",
	Long: `Open a TUI form to edit a VM's configuration and save it to vm.yaml.

If a VM name is supplied the editor opens immediately.
If omitted, the VM list opens and you can navigate to the VM you want to edit.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if len(args) == 1 {
			name = args[0]
		}
		return tui.RunConfigEditor(cmd.Context(), prov, name)
	},
}
