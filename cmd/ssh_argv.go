package cmd

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// sshBoolFlags are the ssh(1) options that take no argument. They may be
// clustered (-vvv, -NT) exactly as ssh allows.
//
// Taken from the ssh(1) synopsis: -46AaCfGgKkMNnqsTtVvXxYy. vee handles the
// destination itself, so -V (print version and exit) is still forwarded — a
// user asking ssh for its version gets it.
const sshBoolFlags = "46AaCfGgKkMNnqsTtVvXxYy"

// sshValueFlags are the ssh(1) options that consume the following argument
// (or the rest of the cluster, as ssh permits: -p22 and -p 22 are the same).
//
// From the ssh(1) synopsis: -B -b -c -D -E -e -F -I -i -J -L -l -m -O -o -P
// -p -Q -R -S -W -w. vee interprets -i and -l itself (they map onto
// --identity and --user) and resolves -p from VM state; the rest pass
// through untouched.
const sshValueFlags = "BbcDEeFIiJLlmOoPpQRSWw"

// sshFlagHelp describes each forwarded ssh flag for shell completion. Keys
// cover sshBoolFlags and sshValueFlags exactly — TestSSHFlagHelpCoversFlags
// fails if a flag is added to either table without a description here.
var sshFlagHelp = map[byte]string{
	'4': "use IPv4 only",
	'6': "use IPv6 only",
	'A': "forward the authentication agent",
	'a': "disable agent forwarding",
	'B': "bind to this interface before connecting",
	'b': "bind to this source address",
	'C': "compress data",
	'c': "cipher spec",
	'D': "dynamic SOCKS forward: [bind:]port",
	'E': "append the debug log to this file",
	'e': "escape character",
	'F': "ssh configuration file",
	'f': "go to background before command execution",
	'G': "print the configuration and exit",
	'g': "let remote hosts connect to local forwarded ports",
	'I': "PKCS#11 shared library",
	'i': "identity (private key) file",
	'J': "connect via this jump host",
	'K': "enable GSSAPI authentication",
	'k': "disable GSSAPI credential forwarding",
	'L': "local forward: [bind:]port:host:hostport",
	'l': "login name (same as --user)",
	'M': "master mode for connection sharing",
	'm': "MAC spec",
	'N': "do not execute a remote command",
	'n': "redirect stdin from /dev/null",
	'O': "control command for the master process",
	'o': "ssh option, e.g. -o ConnectTimeout=5",
	'P': "tag the connection",
	'p': "port (overrides the port vee resolved)",
	'Q': "query supported algorithms",
	'q': "quiet mode",
	'R': "remote forward: [bind:]port:host:hostport",
	'S': "control socket path",
	's': "invoke a subsystem",
	'T': "do not allocate a pty",
	't': "force pty allocation",
	'V': "print the ssh version and exit",
	'v': "verbose ssh output (repeatable: -vv, -vvv)",
	'W': "forward stdin/stdout to host:port",
	'w': "tunnel device: local_tun[:remote_tun]",
	'X': "enable X11 forwarding",
	'x': "disable X11 forwarding",
	'Y': "enable trusted X11 forwarding",
	'y': "log to syslog instead of stderr",
}

// completeSSHArgs is `vee ssh`'s completion function. sshCmd disables cobra's
// flag parsing, so cobra hands over every preceding word — flags included —
// and offers none of its own flag completions. This walks the same grammar
// parseSSHArgv does to decide what the word being typed actually is.
func completeSSHArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Past "--" everything is the remote command, which runs in the guest:
	// the host's files and VM names are both the wrong answer.
	if slices.Contains(args, "--") {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	// A value flag as the previous word means this word is its value. Only
	// -i (identity) and -F/-E/-S (paths) have anything useful to offer, and
	// those are files.
	if n := len(args); n > 0 {
		if prev := args[n-1]; strings.HasPrefix(prev, "-") && !strings.HasPrefix(prev, "--") && len(prev) > 1 {
			last := prev[len(prev)-1]
			if strings.IndexByte(sshValueFlags, last) >= 0 {
				switch last {
				case 'i', 'F', 'E', 'S':
					return nil, cobra.ShellCompDirectiveDefault // file paths
				}
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
		}
	}

	// Completing a flag. Cobra has already offered vee's own (--user,
	// --identity, --ssh-flag) by the time this runs and appends to that list,
	// so only the forwarded ssh flags are added here. They are short-form
	// only, hence nothing to add once the user has typed "--".
	if strings.HasPrefix(toComplete, "-") {
		if strings.HasPrefix(toComplete, "--") {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		// Suggestions must carry everything already typed as their prefix, so
		// that a cluster in progress (-N<TAB> offering -NT) completes rather
		// than replaces. Only bare booleans can be clustered onto: a value
		// flag ends the cluster and takes the remainder as its argument.
		prefix := toComplete
		for i := 1; i < len(prefix); i++ {
			if strings.IndexByte(sshBoolFlags, prefix[i]) < 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
		}
		flags := sshBoolFlags + sshValueFlags
		out := make([]string, 0, len(flags))
		for i := range flags {
			c := flags[i]
			out = append(out, fmt.Sprintf("%s%c\t%s", prefix, c, sshFlagHelp[c]))
		}
		slices.Sort(out)
		return out, cobra.ShellCompDirectiveNoFileComp
	}

	// Otherwise this is the destination — unless one was already given, in
	// which case the only thing that may follow is "--".
	parsed, err := parseSSHArgv(args)
	if err == nil && parsed.Name != "" {
		return []string{"--\trun the rest as a command inside the guest"}, cobra.ShellCompDirectiveNoFileComp
	}
	return completeVMNames(cmd, nil, toComplete)
}

// sshArgv is the result of splitting a `vee ssh` command line into the parts
// vee interprets, the ssh flags it forwards, and the remote command.
type sshArgv struct {
	// Name is the VM name (the ssh "destination" slot).
	Name string
	// User is ssh's -l / vee's --user.
	User string
	// Identity is ssh's -i / vee's --identity.
	Identity string
	// SSHFlags are the flags forwarded to ssh verbatim, in the order given.
	SSHFlags []string
	// Command is the remote command argv, taken from after "--". Empty means
	// an interactive session.
	Command []string
}

// parseSSHArgv splits a `vee ssh` argv the way ssh(1) itself would: flags and
// their arguments first, then a single destination (the VM name), then — after
// "--" — the remote command.
//
// sshCmd disables cobra's flag parsing so that ssh's flag surface is available
// unmodified; this is the parser that replaces it. Flags vee understands are
// lifted out (-l/--user, -i/--identity, --ssh-flag), everything else is
// forwarded to ssh in the order it was given.
func parseSSHArgv(argv []string) (sshArgv, error) {
	var out sshArgv

	for i := 0; i < len(argv); i++ {
		arg := argv[i]

		// Everything after the first bare "--" is the remote command,
		// including anything that looks like a flag.
		if arg == "--" {
			out.Command = argv[i+1:]
			break
		}

		// A non-flag word is the destination. ssh takes exactly one; a
		// second would be the start of a remote command, but vee requires
		// "--" there so the boundary is never ambiguous.
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if out.Name != "" {
				return out, fmt.Errorf("unexpected argument %q after VM name %q; put the remote command after --, e.g. vee ssh %s -- %s", arg, out.Name, out.Name, strings.Join(argv[i:], " "))
			}
			out.Name = arg
			continue
		}

		// Long flags: vee's own, since ssh has none.
		if long, ok := strings.CutPrefix(arg, "--"); ok {
			name, value, hasValue := strings.Cut(long, "=")
			takeValue := func() (string, error) {
				if hasValue {
					return value, nil
				}
				if i+1 >= len(argv) {
					return "", fmt.Errorf("flag --%s needs an argument", name)
				}
				i++
				return argv[i], nil
			}
			switch name {
			case "user":
				v, err := takeValue()
				if err != nil {
					return out, err
				}
				out.User = v
			case "identity":
				v, err := takeValue()
				if err != nil {
					return out, err
				}
				out.Identity = v
			case "ssh-flag":
				v, err := takeValue()
				if err != nil {
					return out, err
				}
				out.SSHFlags = append(out.SSHFlags, v)
			default:
				return out, fmt.Errorf("unknown flag --%s; ssh(1) options are short flags (see vee ssh --help)", name)
			}
			continue
		}

		// Short flags, possibly clustered. ssh allows a value flag to end a
		// cluster and take the remainder as its argument (-p22, -oFoo=bar).
		cluster := arg[1:]
		for j := 0; j < len(cluster); j++ {
			c := cluster[j]
			switch {
			case strings.IndexByte(sshBoolFlags, c) >= 0:
				out.SSHFlags = append(out.SSHFlags, "-"+string(c))
			case strings.IndexByte(sshValueFlags, c) >= 0:
				val := cluster[j+1:]
				if val == "" {
					if i+1 >= len(argv) {
						return out, fmt.Errorf("ssh flag -%c needs an argument", c)
					}
					i++
					val = argv[i]
				}
				switch c {
				case 'l':
					out.User = val
				case 'i':
					out.Identity = val
				default:
					out.SSHFlags = append(out.SSHFlags, "-"+string(c), val)
				}
				j = len(cluster) // the value consumed the rest of the cluster
			default:
				return out, fmt.Errorf("unknown ssh flag -%c in %q", c, arg)
			}
		}
	}

	if out.Name == "" {
		return out, fmt.Errorf("a VM name is required: vee ssh <name>")
	}
	return out, nil
}
