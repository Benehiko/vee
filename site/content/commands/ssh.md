---
title: vee ssh
weight: 50
---

Open an SSH session to a running VM.

```
vee ssh <name> [-- <remote command>]
```

## Flags

| Flag | Description |
|------|-------------|
| `--user` | SSH username (overrides `ssh_user` in `vm.yaml`) |
| `--identity` | Path to SSH private key (defaults to `~/.vee/ssh/id_ed25519`) |

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
```
