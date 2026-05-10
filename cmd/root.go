package cmd

import (
	"os"

	"github.com/Benehiko/vee/internal/tui"
	"github.com/Benehiko/vee/provider"
	"github.com/spf13/cobra"
)

var (
	prov       provider.Provider
	configPath string
)

var rootCmd = &cobra.Command{
	Use:   "vee",
	Short: "QEMU VM manager",
	Long:  "Vee manages QEMU virtual machines with GPU passthrough, virtiofs sharing, and cloud-init templates.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		p, err := provider.NewProvider()
		if err != nil {
			return err
		}
		prov = p
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		p, err := provider.NewProviderSilent()
		if err != nil {
			return err
		}
		return tui.Run(cmd.Context(), p)
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "config file (default ~/.vee/config.yaml)")
	rootCmd.AddCommand(createCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(deleteCmd)
	rootCmd.AddCommand(sshShareCmd)
	rootCmd.AddCommand(sshCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(portsCmd)
	rootCmd.AddCommand(tunnelCmd)
	rootCmd.AddCommand(ipCmd)
	rootCmd.AddCommand(configCmd)
}
