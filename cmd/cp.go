package cmd

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Benehiko/vee/internal/vm"
)

var (
	cpRecursive bool
	cpUser      string
)

var cpCmd = &cobra.Command{
	Use:   "cp <src> <dst>",
	Short: "Copy files between the host and a running VM",
	Long: `Copies a file or directory between the host and a running VM over scp.

Exactly one side names the guest, as <vm>:<path>. An empty guest path
(<vm>:) means the login user's home directory. Windows guest paths keep
their drive letter: only the first colon separates the VM name.

The username defaults to the cloud-init user configured at creation time
(override with --user), authenticating with the vee SSH key.

Examples:
  vee cp ./e2e.test myvm:/home/dev/e2e.test
  vee cp myvm:/var/log/syslog ./syslog
  vee cp -r ./testdata myvm:
  vee cp winvm:C:\Users\vee\out.txt .`,
	Args:              cobra.ExactArgs(2),
	ValidArgsFunction: completeCpArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		windowsHost := runtime.GOOS == "windows"
		srcVM, srcPath, srcRemote := parseCpArg(args[0], windowsHost)
		dstVM, dstPath, dstRemote := parseCpArg(args[1], windowsHost)

		switch {
		case srcRemote && dstRemote:
			return fmt.Errorf("both %q and %q name a VM; one side must be a host path", args[0], args[1])
		case !srcRemote && !dstRemote:
			return fmt.Errorf("neither argument names a VM; use <vm>:<path> on one side (prefix a local path containing a colon with ./)")
		}

		spec := vm.CopySpec{
			Recursive: cpRecursive,
			User:      cpUser,
		}
		if srcRemote {
			spec.VMName, spec.GuestPath, spec.HostPath = srcVM, srcPath, dstPath
		} else {
			spec.VMName, spec.GuestPath, spec.HostPath = dstVM, dstPath, srcPath
			spec.ToGuest = true
		}

		return vm.NewManager(prov).CopyPath(cmd.Context(), spec, os.Stdout, os.Stderr)
	},
}

// parseCpArg splits one cp argument into (vmName, path, remote), following
// scp's own rules: an argument with no colon is local, and so is one whose
// first colon comes after a path separator (./odd:name). On Windows hosts a
// single-letter prefix before the colon is a drive path, not a VM name. For
// remote arguments only the first colon delimits, so Windows guest paths keep
// their drive letter (winvm:C:\x → vm "winvm", path "C:\x").
func parseCpArg(arg string, windowsHost bool) (vmName, path string, remote bool) {
	name, rest, ok := strings.Cut(arg, ":")
	if !ok {
		return "", arg, false
	}
	if strings.ContainsAny(name, `/`) || (windowsHost && strings.ContainsAny(name, `\`)) {
		return "", arg, false
	}
	if windowsHost && len(name) == 1 {
		return "", arg, false
	}
	return name, rest, true
}

// completeCpArgs offers VM names (colon-suffixed) alongside default file
// completion, for both positions.
func completeCpArgs(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) >= 2 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names, _ := completeVMNames(nil, nil, "")
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n + ":"
	}
	// ShellCompDirectiveNoSpace keeps the cursor glued to the colon;
	// ShellCompDirectiveDefault keeps file completion for local paths.
	return out, cobra.ShellCompDirectiveNoSpace | cobra.ShellCompDirectiveDefault
}

func init() {
	cpCmd.Flags().BoolVarP(&cpRecursive, "recursive", "r", false, "Copy directories recursively")
	cpCmd.Flags().StringVarP(&cpUser, "user", "u", "", "SSH username (default: cloud-init user)")
	rootCmd.AddCommand(cpCmd)
}
