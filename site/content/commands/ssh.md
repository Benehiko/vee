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

### Windows guests

What re-splits the command inside the guest is cmd.exe (the shell Windows'
sshd hands exec requests to), not a POSIX shell, so for Windows guests vee
quotes for cmd.exe instead: arguments pass bare where possible — backslash
paths like `C:\Users\vee\run.bat` included — and are double-quoted by Windows
argv rules where not. Single quotes are never emitted; cmd.exe treats `'` as a
literal character.

```sh
vee ssh winvm -- cmdkey /list
vee ssh winvm -- powershell -NoProfile -File 'C:\scripts\setup.ps1'
```

(The single quotes in the second example are consumed by the local shell; the
guest receives the bare path.)

Two cmd.exe quirks no quoting can hide: `%NAME%` expands even inside double
quotes, and shell operators arrive as literal argument text — to use `&`, `|`
or redirection, invoke a shell explicitly, just as on Linux:

```sh
vee ssh winvm -- cmd /c "dir & ver"
vee ssh winvm -- powershell -Command "Get-Process | Select-Object -First 3"
```

One caveat on the second form: `powershell -Command` re-parses its whole
command line by PowerShell's own rules (its only quote escape is `\"`), so a
`-Command` one-liner with *embedded* double quotes may not survive verbatim.
That is PowerShell behavior, not the transport — arguments to `-File` scripts
arrive exactly as typed, and `-EncodedCommand` sidesteps quoting entirely.

## IP resolution

vee first checks whether something is actually listening on the VM's recorded loopback SSH port — QEMU's user-mode NAT port-forward, or the [daemon's loopback proxy]({{< relref "daemon" >}}#ssh-loopback-proxies) for bridge-mode VMs — and connects to `127.0.0.1` if so. A recorded port that nothing serves (for example, the daemon is stopped) is skipped rather than trusted.

Otherwise vee resolves the VM's LAN address:

1. **ARP / neighbour table** — matches the VM's MAC address to an IP in the host's neighbour table. IPv4 is preferred over IPv6 link-local.
2. **Guest agent** — if `guest_agent: true` and `qemu-guest-agent` is installed, reads the IP directly from the guest without ARP.

## Username

The login account is resolved in order:

1. `--user` / `-u` / `-l` on the command line
2. `ssh_user` in `vm.yaml`
3. The cloud-init user created at build time (`cloud_init.user`)
4. The distro image's default user (`cloud_init.default_user`) — templates
   that don't create a user (`docker`, `desktop`, `dns-sink`) inject the SSH
   keys into this account instead, e.g. `alpine` on Alpine-based templates

Only if all four are empty does ssh fall back to its own default, the local
host username.

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
