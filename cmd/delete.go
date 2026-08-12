package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/vm"
)

var deleteCmd = &cobra.Command{
	Use:               "delete <name>...",
	Short:             "Delete one or more VMs and their disks",
	Long:              "Deletes the VM configuration, disks, and runtime state. The backups/ directory is preserved at ~/.vee/vms/<name>/backups/.",
	Args:              cobra.MinimumNArgs(1),
	ValidArgsFunction: completeVMNamesMulti,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := vm.NewManager(prov)
		var errs []error
		for _, name := range args {
			if err := mgr.Delete(name); err != nil {
				errs = append(errs, fmt.Errorf("delete %s: %w", name, err))
			}
		}
		return errors.Join(errs...)
	},
}
