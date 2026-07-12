package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/vm"
)

var checkCmd = &cobra.Command{
	Use:               "check <name>",
	Short:             "Run health checks on an installed VM and show results",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		mgr := vm.NewManager(prov)
		checks, err := mgr.RunHealthCheck(cmd.Context(), name)
		if err != nil {
			return err
		}
		printHealthChecks(checks)
		return nil
	},
}

func printHealthChecks(checks []vm.HealthCheck) {
	pass := 0
	for _, c := range checks {
		if c.OK {
			pass++
		}
	}
	fmt.Printf("health checks: %d/%d passed\n\n", pass, len(checks))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "CHECK\tSTATUS\tDETAIL")
	for _, c := range checks {
		status := "PASS"
		if !c.OK {
			status = "FAIL"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", c.Name, status, c.Detail)
	}
	_ = w.Flush()
}

func init() {
	rootCmd.AddCommand(checkCmd)
}
