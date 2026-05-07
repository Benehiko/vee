package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/Benehiko/vee/vm"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all VMs",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := vm.NewManager(prov)
		entries, err := mgr.List()
		if err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "NAME\tTEMPLATE\tMEMORY\tCPUs\tSTATUS\tPID\tSPICE")
		for _, e := range entries {
			status := "stopped"
			if e.State.Running {
				status = "running"
			}
			pid := "-"
			if e.State.PID > 0 {
				pid = fmt.Sprintf("%d", e.State.PID)
			}
			spice := "-"
			if e.State.SPICEPort > 0 {
				spice = fmt.Sprintf("%d", e.State.SPICEPort)
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
				e.Config.Name,
				e.Config.Template,
				e.Config.Memory,
				e.Config.CPUs,
				status,
				pid,
				spice,
			)
		}
		return w.Flush()
	},
}
