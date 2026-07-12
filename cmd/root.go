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
	verbose    bool
	mirrorFlag string
)

var rootCmd = &cobra.Command{
	Use:   "vee",
	Short: "QEMU VM manager",
	Long:  "Vee manages QEMU virtual machines with GPU passthrough, virtiofs sharing, and cloud-init templates.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		p, err := provider.New(verbose)
		if err != nil {
			return err
		}
		// --mirror overrides the config file's mirror_mode when set.
		if cmd.Flags().Changed("mirror") {
			p.Config().MirrorMode = mirrorFlag
		}
		prov = p
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return tui.Run(cmd.Context(), prov)
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "config file (default ~/.vee/config.yaml)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Stream logs to stderr (default: file only at ~/.vee/logs/vee.log)")
	rootCmd.PersistentFlags().StringVar(&mirrorFlag, "mirror", "auto", "Pacman mirror cache: auto|on|off")
	rootCmd.AddCommand(createCmd)
	rootCmd.AddCommand(pullCmd)
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
	rootCmd.AddCommand(backupCmd)
	rootCmd.AddCommand(autostartCmd)
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(mirrorCmd)
	rootCmd.AddCommand(runnerCmd)
}
