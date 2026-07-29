package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/buildinfo"
	"github.com/Benehiko/vee/internal/vzhelper"
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
		// The helper is installed separately and can drift from this binary, so
		// report it as its own identity rather than implying they match.
		if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
			fmt.Print(helperVersionLine(cmd.Context(), v))
		}
		return nil
	},
}

func resolveVersion() (v, c, d string) {
	return buildinfo.Resolve(version, commit, date)
}

// helperVersionTimeout bounds the helper query. `vee version` must stay
// instant even when the helper cannot run at all.
const helperVersionTimeout = 3 * time.Second

// helperVersionLine describes the vee-vz-helper this vee would use. Both the
// path and the version matter: vee picks the helper from four locations in
// order, so a user can easily be running one they did not mean to.
//
// Every failure is reported rather than returned — not having a helper is
// normal for anyone who does not run macOS guests, and `vee version` must never
// fail because of it.
func helperVersionLine(ctx context.Context, veeVersion string) string {
	// Deliberately does not heal the binary the way ResolveHelper does:
	// reporting a version must not modify anything on disk.
	path, err := vzhelper.FindHelper()
	if err != nil {
		return "  helper: not installed (only needed for macOS guests)\n"
	}

	queryCtx, cancel := context.WithTimeout(ctx, helperVersionTimeout)
	defer cancel()
	//nolint:gosec // path comes from vee's own helper resolution
	out, err := exec.CommandContext(queryCtx, path, "--version").Output()
	if err != nil {
		return fmt.Sprintf("  helper: %s (could not report its version: %v)\n", path, err)
	}
	helperVersion := strings.TrimSpace(string(out))

	line := fmt.Sprintf("  helper: %s\n          path:   %s\n", helperVersion, path)
	// Compare the version alone: the helper reports its commit and build time
	// too, and two builds of the same commit have different timestamps.
	if fields := strings.Fields(helperVersion); len(fields) > 0 && fields[0] != veeVersion {
		line += "          note:   the helper is a different build from vee\n"
	}
	return line
}

func init() {
	versionCmd.Flags().BoolVar(&versionShort, "short", false, "Print only the version string")
	rootCmd.AddCommand(versionCmd)
}
