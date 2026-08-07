package cmd

import (
	"fmt"
	"strings"
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
