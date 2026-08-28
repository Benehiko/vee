# QMP command tooling (`vee qmp`, `vee screenshot`)

`vee qmp` sends [QMP](https://qemu.readthedocs.io/en/latest/interop/qmp-spec.html)
(QEMU Machine Protocol) commands to a running VM and prints the JSON `return`
payload. It exists so you don't have to script against the raw QMP socket by
hand — vee already speaks QMP internally, and this exposes that channel.

## Usage

```sh
# Query run state
vee qmp myvm query-status

# A command that takes arguments (--args is a JSON object)
vee qmp myvm human-monitor-command --args '{"command-line":"info registers"}'

# Pipe one or more full QMP request objects on stdin
echo '{"execute":"query-block"}' | vee qmp myvm --stdin

# Compact single-line output, e.g. to pipe into jq
vee qmp myvm --raw query-status | jq .status
```

### Flags

| Flag | Description |
|------|-------------|
| `--args <json>` | JSON object passed as the command's QMP `arguments` |
| `--stdin` | Read one or more QMP request objects (one JSON object each) from stdin instead of positional args |
| `--raw` | Emit compact single-line JSON instead of pretty-printed output |
| `--timeout <dur>` | How long to wait for the QMP socket / daemon to respond (default `3s`) |

With `--stdin` you can run several commands in one invocation; each object must
have an `execute` field and may have an `arguments` object, mirroring the
on-the-wire QMP request shape.

## Screenshots (`vee screenshot`)

`vee screenshot` captures a running VM's display to a PNG file, using QMP's
`screendump` command under the hood. It works headless — nothing needs to be
attached to the VM's display, so it is handy for checking what an installer or
desktop session is showing without opening a SPICE/VNC client.

```sh
# Timestamped PNG in the current directory (path printed on stdout)
vee screenshot myvm

# Explicit destination
vee screenshot myvm /tmp/desktop.png

# Scriptable: stdout carries only the written file's absolute path
open "$(vee screenshot myvm)"
```

QEMU's `screendump` emits a PPM image; vee converts it to PNG internally, so
no external tooling (`sips`, ImageMagick) is needed. The command uses the same
daemon-aware transport as `vee qmp` (see below).

Only QEMU-backed VMs can be captured — vz-backed VMs have no QMP socket. For
a macOS guest, connect to its Screen Sharing with `vee view` instead.

The MCP server exposes the same capture as the `vm_screenshot` tool, which
returns the PNG inline as image content so an agent can look at the guest's
screen directly.

## How it reaches QEMU

QEMU's QMP socket (`-qmp unix:…,server,nowait`) accepts **only one connected
client at a time**. The vee daemon holds that single connection for every VM it
supervises, so it can watch for guest-initiated `SHUTDOWN` events (telling a
clean guest poweroff apart from a crash) and `RESET` events (a `reboot` inside
the guest, which vee answers with a full power-cycle — see `vee stop`'s
documentation). A second process dialing the same socket gets `EAGAIN`
("resource temporarily unavailable").

To avoid that collision, `vee qmp` chooses its transport automatically:

- **Daemon running** → the command is routed through the daemon's control
  socket (`~/.vee/daemon.sock`, owner-only `0600`). The daemon executes the
  command on the connection it already owns and returns the JSON result. This is
  the normal path, because most setups run the daemon.
- **No daemon** → `vee qmp` dials the VM's QMP socket directly. This works for a
  VM started by a standalone `vee start` when no daemon is supervising it.

Internally the daemon uses a single **QMP owner** per VM: one goroutine owns the
socket and multiplexes it between synchronous command execution and asynchronous
event delivery. Commands are serialized (QMP is request/response with one
command in flight at a time); events are dispatched to the shutdown watcher as
they arrive. The same routing is used by `vee stop` so a graceful
`system_powerdown` reaches the guest whether or not the daemon owns the socket.

## Safety notes

- The control socket exposes QMP command execution against your local VMs. It is
  created with `0600` permissions (owner only) under `~/.vee`.
- QMP is powerful: commands like `human-monitor-command`, `migrate`, or block
  operations can disrupt or destroy a running guest. `vee qmp` does not
  whitelist commands — treat it like direct QEMU monitor access.
