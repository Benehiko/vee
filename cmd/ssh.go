package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/Benehiko/vee/vm"
	"github.com/spf13/cobra"
)

var (
	sshUser       string
	sshIdentity   string
	sshExtraFlags []string
)

var sshCmd = &cobra.Command{
	Use:               "ssh <name>",
	Short:             "Open an SSH session into a running VM",
	ValidArgsFunction: completeVMNames,
	Long: `Connects to a running VM via SSH.

For headless VMs with a port-forward (--ssh-port), connects to 127.0.0.1 on
that port. For bridge-mode VMs, resolves the guest IP from the ARP/neighbour
table using the VM's MAC address.

The username defaults to the cloud-init user configured at creation time.
Override with --user. Pass extra ssh(1) flags after --.

Examples:
  vee ssh myvm
  vee ssh myvm --user root
  vee ssh myvm --identity ~/.ssh/id_ed25519
  vee ssh myvm -- -L 8080:localhost:8080`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		extra := args[1:]

		mgr := vm.NewManager(prov)
		entries, err := mgr.List()
		if err != nil {
			return err
		}

		var entry *vm.ListEntry
		for _, e := range entries {
			if e.Config.Name == name {
				entry = e
				break
			}
		}
		if entry == nil {
			return fmt.Errorf("VM %q not found", name)
		}
		if !entry.State.Running {
			return fmt.Errorf("VM %q is not running", name)
		}

		user := sshUser
		if user == "" {
			user = entry.Config.SSHUser
		}
		if user == "" && entry.Config.CloudInit != nil && entry.Config.CloudInit.User != "" {
			user = entry.Config.CloudInit.User
		}

		// For TrueNAS, default to stored admin user.
		if entry.Config.Template == "truenas" && user == "" {
			user = entry.Config.TrueNASUser
		}

		// Always prefer the vee SSH keypair when no identity is specified.
		if sshIdentity == "" {
			home, herr := os.UserHomeDir()
			if herr == nil {
				veeKey := home + "/.vee/ssh/id_ed25519"
				if _, statErr := os.Stat(veeKey); statErr == nil {
					sshIdentity = veeKey
				}
			}
		}

		var host string
		var port int

		switch {
		case entry.State.SSHPort > 0:
			// Headless user-mode port-forward.
			host = "127.0.0.1"
			port = entry.State.SSHPort

		case entry.Config.NIC.Mode == "bridge" || entry.Config.NIC.Mode == "":
			// Bridge mode — try to resolve IP from neighbour table via MAC.
			mac := entry.Config.NIC.MAC
			if mac == "" {
				return fmt.Errorf("VM %q has no MAC address recorded; cannot resolve IP", name)
			}
			ip, resolveErr := resolveIPFromMAC(mac)
			if resolveErr != nil {
				return fmt.Errorf("could not resolve IP for VM %q (MAC %s): %w\nTry: ssh %s<ip>", name, mac, resolveErr, userPrefix(user))
			}
			host = ip
			port = 22

		default:
			return fmt.Errorf("VM %q has no SSH port and is not on a bridge network; check --ssh-port or --nic-mode", name)
		}

		sshArgs := buildSSHArgs(user, host, port, sshIdentity, extra, sshExtraFlags)

		sshBin, err := exec.LookPath("ssh")
		if err != nil {
			return fmt.Errorf("ssh not found in PATH: %w", err)
		}

		// Replace the current process with ssh so signals flow naturally.
		return syscall.Exec(sshBin, append([]string{"ssh"}, sshArgs...), os.Environ())
	},
}

func buildSSHArgs(user, host string, port int, identity string, positional, extra []string) []string {
	var args []string
	if port != 22 {
		args = append(args, "-p", fmt.Sprintf("%d", port))
	}
	if identity != "" {
		args = append(args, "-i", identity)
	}
	// extra holds --ssh-flag values (ssh flags, e.g. -L 8080:...) — before host.
	args = append(args, extra...)

	dest := host
	if user != "" {
		dest = user + "@" + host
	}
	args = append(args, dest)

	// positional holds remote command args — after host.
	args = append(args, positional...)
	return args
}

func resolveIPFromMAC(mac string) (string, error) {
	return vm.ResolveIPFromMAC(mac)
}

func userPrefix(user string) string {
	if user == "" {
		return ""
	}
	return user + "@"
}

func init() {
	sshCmd.Flags().StringVarP(&sshUser, "user", "u", "", "SSH username (default: cloud-init user)")
	sshCmd.Flags().StringVarP(&sshIdentity, "identity", "i", "", "SSH identity file (private key)")
	sshCmd.Flags().StringArrayVar(&sshExtraFlags, "ssh-flag", nil, "Extra flags passed to ssh(1) (repeatable)")
}
