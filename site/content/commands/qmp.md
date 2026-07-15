---
title: vee qmp
weight: 95
---

Send a [QMP (QEMU Machine Protocol)](https://www.qemu.org/docs/master/interop/qmp-spec.html) command to a running VM and print the JSON `return` payload.

```
vee qmp <name> [command]
```

QMP is the same control channel vee speaks internally. `vee qmp` exposes it directly, so you can query or drive QEMU without going through a higher-level `vee` command.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--args` | `""` | JSON object passed as the command's QMP `arguments` |
| `--raw` | `false` | Emit compact single-line JSON instead of pretty-printed output |
| `--stdin` | `false` | Read one or more QMP request objects from stdin instead of positional args |
| `--timeout` | `3s` | How long to wait for the QMP socket/daemon to respond |

## Daemon routing

QEMU's QMP socket accepts only one connected client at a time, and the [vee daemon]({{< relref "daemon" >}}) holds that connection for every VM it supervises (to watch for guest `SHUTDOWN` events).

- **Daemon running** — the command is routed through the daemon's control socket (`~/.vee/daemon.sock`, owner-only `0600`); the daemon runs it on the connection it already owns and returns the result. This is the normal path.
- **No daemon** — `vee qmp` dials the VM's QMP socket directly (for a VM started by a standalone `vee start`).

## Examples

```sh
# Query the QEMU version
vee qmp myvm query-version

# Query VM run state
vee qmp myvm query-status

# Command with arguments
vee qmp myvm device_add --args '{"driver":"virtio-net-pci","id":"net1"}'

# Pipe a full request object
echo '{"execute":"query-block"}' | vee qmp myvm --stdin

# Compact output for scripting
vee qmp myvm query-name --raw
```

## Safety

`vee qmp` does **not** whitelist commands. `human-monitor-command`, `migrate`, and block-device operations can disrupt or destroy a running guest — treat it like direct QEMU monitor access.

See [docs/qmp.md](https://github.com/Benehiko/vee/blob/main/docs/qmp.md) for the daemon transport internals.
