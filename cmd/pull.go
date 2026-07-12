package cmd

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/images"
)

var pullList bool

var pullCmd = &cobra.Command{
	Use:   "pull <distro> [version]",
	Short: "Download (or build) a base image ISO into the local cache",
	Long: `Download a distro image into the local image cache so it can be reused by VMs.

The image is fetched once and stored under the vee image cache. Subsequent
'vee create' calls that need the same image reuse the cached copy instead of
downloading it again. If the image is already cached, pull is a no-op.

Version may be a specific version string or 'latest' (the default). Windows
images are assembled from Microsoft's servers via UUP dump inside a container
and require nerdctl or docker on PATH.

Accepted argument forms:
  vee pull ubuntu               — newest known Ubuntu
  vee pull ubuntu 24.04         — a specific version
  vee pull ubuntu-24.04         — same, as a single token
  vee pull windows win10        — build the Windows 10 ISO
  vee pull --list               — list every pullable image

Run 'vee pull --list' to see all supported distros and versions.`,
	Args:              cobra.MaximumNArgs(2),
	ValidArgsFunction: completePullArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if pullList {
			return printPullList(cmd)
		}
		if len(args) == 0 {
			return fmt.Errorf("specify a distro to pull (e.g. 'vee pull ubuntu') or use --list")
		}

		distro, version := parsePullArgs(args)
		if !isSupportedDistro(distro) {
			return fmt.Errorf("unknown distro %q; supported: %s", distro, strings.Join(images.SupportedDistros(), ", "))
		}

		img, err := images.NewImage(prov, distro, version)
		if err != nil {
			return err
		}

		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Pulling %s %s...\n", img.Distro(), img.Version())
		if err := img.Download(cmd.Context()); err != nil {
			return fmt.Errorf("pull %s %s: %w", distro, version, err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Ready: %s\n", img.AbsolutePath())
		return nil
	},
}

// parsePullArgs turns the CLI args into a (distro, version) pair. It accepts
// either two tokens ("ubuntu" "24.04") or a single hyphenated token
// ("ubuntu-24.04"). A missing version resolves to "latest".
func parsePullArgs(args []string) (distro, version string) {
	if len(args) == 2 {
		return args[0], args[1]
	}
	tok := args[0]
	// Split on the first hyphen only if the prefix is a known distro, so that
	// version strings containing hyphens (none today, but future-proof) survive.
	if i := strings.Index(tok, "-"); i > 0 {
		if isSupportedDistro(tok[:i]) {
			return tok[:i], tok[i+1:]
		}
	}
	return tok, "latest"
}

func isSupportedDistro(distro string) bool {
	return slices.Contains(images.SupportedDistros(), distro)
}

func printPullList(cmd *cobra.Command) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "DISTRO\tVERSIONS (newest first)")
	distros := images.SupportedDistros()
	sort.Strings(distros)
	for _, d := range distros {
		versions := images.DistroVersions(d)
		_, _ = fmt.Fprintf(w, "%s\t%s\n", d, strings.Join(versions, ", "))
	}
	return w.Flush()
}

// completePullArgs completes the distro (first arg, including hyphenated
// distro-version forms) and the version (second arg, scoped to the distro).
func completePullArgs(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		var out []string
		for _, d := range images.SupportedDistros() {
			out = append(out, d)
			for _, v := range images.DistroVersions(d) {
				out = append(out, d+"-"+v)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	case 1:
		versions := images.DistroVersions(args[0])
		out := append([]string{"latest"}, versions...)
		return out, cobra.ShellCompDirectiveNoFileComp
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

func init() {
	pullCmd.Flags().BoolVar(&pullList, "list", false, "List all supported distros and versions instead of pulling")
}
