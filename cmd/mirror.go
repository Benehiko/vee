package cmd

import (
	"fmt"

	"github.com/Benehiko/vee/internal/mirror"
	"github.com/spf13/cobra"
)

var mirrorCmd = &cobra.Command{
	Use:   "mirror",
	Short: "Manage the host-side pacman caching proxy (pacoloco)",
	Long: `Manage the host-side pacman caching proxy used by Arch VMs.

The proxy (pacoloco) runs as a systemd --user unit on localhost:9129.
Arch guests built with --mirror=on (or auto, with the proxy active) are
configured to fetch packages through it, so identical packages are not
re-downloaded from upstream Arch mirrors on every VM creation.`,
}

var mirrorStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Install (if needed) and start the pacoloco user unit",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := mirror.Start(cmd.Context()); err != nil {
			return err
		}
		s, err := mirror.GetStatus(cmd.Context())
		if err != nil {
			return err
		}
		printStatus(cmd, s)
		return nil
	},
}

var mirrorStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop and disable the pacoloco user unit",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return mirror.Stop(cmd.Context())
	},
}

var mirrorStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show pacoloco unit state and cache size",
	RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := mirror.GetStatus(cmd.Context())
		if err != nil {
			return err
		}
		printStatus(cmd, s)
		return nil
	},
}

var mirrorPurgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Delete all cached packages on disk",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := mirror.Purge(); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "pacoloco cache purged")
		return nil
	},
}

func init() {
	mirrorCmd.AddCommand(mirrorStartCmd)
	mirrorCmd.AddCommand(mirrorStopCmd)
	mirrorCmd.AddCommand(mirrorStatusCmd)
	mirrorCmd.AddCommand(mirrorPurgeCmd)
}

func printStatus(cmd *cobra.Command, s *mirror.Status) {
	w := cmd.OutOrStdout()
	state := "inactive"
	if s.Active {
		state = "active"
	}
	_, _ = fmt.Fprintf(w, "Unit:      %s (%s)\n", mirror.UnitName, state)
	_, _ = fmt.Fprintf(w, "Installed: %t\n", s.Installed)
	_, _ = fmt.Fprintf(w, "Listener:  http://127.0.0.1:%d/\n", mirror.DefaultPort)
	_, _ = fmt.Fprintf(w, "Guest URL: %s\n", mirror.GuestMirrorURL(""))
	_, _ = fmt.Fprintf(w, "Cache dir: %s\n", s.Paths.CacheDir)
	_, _ = fmt.Fprintf(w, "Cache size: %.2f MiB\n", float64(s.CacheSize)/(1024*1024))
}
