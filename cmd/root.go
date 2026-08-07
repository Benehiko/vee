package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/tui"
	"github.com/Benehiko/vee/provider"
)

var (
	prov       provider.Provider
	configPath string
	verbose    bool
	mirrorFlag string
)

var rootCmd = &cobra.Command{
	// Runtime failures are not usage errors — printing the full flag list
	// after, say, a failed VM start buries the actual message.
	SilenceUsage: true,
	Use:          "vee",
	Short:        "QEMU VM manager",
	Long:         "Vee manages QEMU virtual machines with GPU passthrough, virtiofs sharing, and cloud-init templates.",
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
	// Ctrl-C / SIGTERM cancel the command context so long-running child
	// processes (image downloads, the vz restore helper) are killed and
	// their cleanup paths run instead of being orphaned mid-write.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := rootCmd.ExecuteContext(ctx)
	stop()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "config file (default ~/.vee/config.yaml)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Log at debug level and stream to stderr (default: info, file only at ~/.vee/logs/vee.log)")
	rootCmd.PersistentFlags().StringVar(&mirrorFlag, "mirror", "auto", "Pacman mirror cache: auto|on|off")
	rootCmd.AddCommand(createCmd)
	rootCmd.AddCommand(pullCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(deleteCmd)
	rootCmd.AddCommand(moveCmd)
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
