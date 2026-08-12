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
	return listVMNames(nil)
}

// completeVMNamesMulti completes VM names for commands taking several,
// omitting names already present on the command line.
func completeVMNamesMulti(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	return listVMNames(args)
}

func listVMNames(exclude []string) ([]string, cobra.ShellCompDirective) {
	p, err := provider.New(false)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	mgr := vm.NewManager(p)
	entries, err := mgr.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	seen := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		seen[name] = true
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !seen[e.Config.Name] {
			names = append(names, e.Config.Name)
		}
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
