---
title: vee ssh
weight: 50
---

Open an SSH session to a running VM.

```
vee ssh [ssh flags] <name> [-- <remote command>]
```

## Flags

| Flag | Description |
|------|-------------|
| `--user`, `-u`, `-l` | SSH username (overrides `ssh_user` in `vm.yaml`) |
| `--identity`, `-i` | Path to SSH private key (defaults to `~/.vee/ssh/id_ed25519`) |
| `--ssh-flag` | An extra flag to pass to `ssh` (repeatable) |

### ssh(1) flags

`vee ssh` accepts ssh's own flags directly, before the VM name — `-L`, `-R`,
`-D`, `-J`, `-A`, `-o`, `-v` and the rest of the ssh flag surface are passed
through untouched, including clustered forms such as `-NT` and `-vvv`.

Because `-v` after the subcommand belongs to ssh, vee's own global flags
(`--verbose`, `--config`, `--mirror`) have to come *before* it:

```sh
vee -v ssh myvm     # vee's verbose logging
vee ssh -v myvm     # ssh's verbose output
```

Shell completion knows this grammar: it offers the ssh flags (and completes
clustered forms such as `-N` into `-NT`), VM names in the destination slot,
`--` once a VM name is given, and nothing after `--`, since the remote command
runs in the guest rather than on the host.

Where vee and ssh set the same option, vee's value is a default the flag
overrides: an explicit `-p` wins over the port vee resolved, and an explicit
`-o` wins over vee's own `-o` defaults.

## Remote commands

Everything after `--` is the remote command. Each argument is shell-quoted
before being handed to ssh, so the guest sees exactly the argument vector
given on the host — quoting, spaces and shell metacharacters all survive:

```sh
vee ssh myvm -- sh -c 'echo "x  y"; echo done'
```

Without that quoting, ssh would join the arguments with spaces and let the
remote shell re-split them, which silently mangles or drops multi-word
arguments.

## IP resolution

vee resolves the VM's IP address in this order:

1. **Guest agent** — if `guest_agent: true` and `qemu-guest-agent` is installed, reads the IP directly from the guest without ARP. This is the most reliable method.
2. **ARP / neighbour table** — matches the VM's MAC address to an IP in the host's neighbour table. IPv4 is preferred over IPv6 link-local.
3. **NAT port forward** — for user-mode NAT VMs, connects via `127.0.0.1` and the forwarded SSH port.

## SSH key

vee generates an Ed25519 keypair at `~/.vee/ssh/id_ed25519` on first use and injects the public key via cloud-init. For bridge VMs without cloud-init, set `ssh_user` in `vm.yaml` and ensure the key is pre-installed in the guest.

## Examples

```sh
# Interactive shell
vee ssh myvm

# Run a single command
vee ssh myvm -- uptime

# Override user
vee ssh --user root myvm

# Forward a local port for the life of the session
vee ssh -L 8080:localhost:8080 myvm

# Run a command whose arguments contain spaces and quotes
vee ssh myvm -- sh -c 'echo "x  y"; echo done'
```
