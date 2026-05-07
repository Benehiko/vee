package cmd

import (
	"fmt"
	"time"

	veeserver "github.com/Benehiko/vee/server"
	"github.com/Benehiko/vee/vm"
	"github.com/spf13/cobra"
)

var dashboardAddr string

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Start a web dashboard for all VMs",
	Long:  "Serves a live HTML dashboard and JSON API at the given address.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr := vm.NewManager(prov)
		d := veeserver.NewDashboard(mgr)

		go d.Poll(cmd.Context(), 2*time.Second)

		fmt.Printf("Dashboard listening on http://%s\n", dashboardAddr)
		return d.Listen(cmd.Context(), dashboardAddr)
	},
}

func init() {
	dashboardCmd.Flags().StringVar(&dashboardAddr, "addr", "127.0.0.1:7777", "Address to listen on")
	rootCmd.AddCommand(dashboardCmd)
}
