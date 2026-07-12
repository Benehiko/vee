package cmd

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Build-time overrides. The Makefile and CI builds inject these via -ldflags
// "-X github.com/Benehiko/vee/cmd.version=...". When unset (e.g. plain
// `go install`), values are filled in from runtime/debug.ReadBuildInfo so the
// binary still reports a useful identity.
var (
	version = ""
	commit  = ""
	date    = ""
)

var versionShort bool

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print vee version information",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		v, c, d := resolveVersion()
		if versionShort {
			fmt.Println(v)
			return nil
		}
		fmt.Printf("vee %s\n", v)
		fmt.Printf("  commit: %s\n", c)
		fmt.Printf("  built:  %s\n", d)
		fmt.Printf("  go:     %s\n", runtime.Version())
		fmt.Printf("  os:     %s/%s\n", runtime.GOOS, runtime.GOARCH)
		return nil
	},
}

func resolveVersion() (v, c, d string) {
	v, c, d = version, commit, date

	info, ok := debug.ReadBuildInfo()
	if !ok {
		if v == "" {
			v = "unknown"
		}
		if c == "" {
			c = "unknown"
		}
		if d == "" {
			d = "unknown"
		}
		return v, c, d
	}

	if v == "" {
		v = info.Main.Version
		if v == "" || v == "(devel)" {
			v = "dev"
		}
	}
	if c == "" || d == "" {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if c == "" {
					c = s.Value
				}
			case "vcs.time":
				if d == "" {
					d = s.Value
				}
			case "vcs.modified":
				if s.Value == "true" && c != "" {
					c += "-dirty"
				}
			}
		}
	}
	if c == "" {
		c = "unknown"
	}
	if d == "" {
		d = "unknown"
	}
	return v, c, d
}

func init() {
	versionCmd.Flags().BoolVar(&versionShort, "short", false, "Print only the version string")
	rootCmd.AddCommand(versionCmd)
}
