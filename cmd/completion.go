package cmd

import (
	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

func completeVMNames(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	p, err := provider.New(false)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	mgr := vm.NewManager(p)
	entries, err := mgr.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Config.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
