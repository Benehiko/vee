# MCP server — driving vee from coding agents

`vee mcp` runs a [Model Context Protocol](https://modelcontextprotocol.io) server
over stdio, exposing vee as typed tools. Coding agents (Claude Code, Cursor,
Codex, and any other MCP-capable client) can create, boot, inspect, back up,
and command VMs directly instead of shelling out to the CLI and parsing
tab-aligned text.

## Registering the server

The server is the ordinary `vee` binary — no extra install. Register it with
your agent as a stdio server:

```sh
# Claude Code
claude mcp add vee -- vee mcp

# Codex CLI
codex mcp add vee -- vee mcp
```

Or in a JSON-based MCP client configuration:

```json
{
  "mcpServers": {
    "vee": { "command": "vee", "args": ["mcp"] }
  }
}
```

The server runs with the privileges of the invoking user and needs the same
host setup as the CLI (see [prerequisites.md](prerequisites.md)). There is no
network listener: the only transport is stdio, so nothing is exposed beyond
the process the agent spawned.

## Feature parity and the drift guard

The MCP surface mirrors the CLI: **every vee command either maps to MCP
tools or carries an explicit exemption** stating why it stays CLI-only
(interactive TUIs, long-lived foreground processes, root-mutating host
operations). The mapping lives in `internal/mcpserver/coverage.go` and is
enforced by `TestMCPCoversCLI`, which walks the real cobra command tree —
adding a CLI command without deciding its MCP surface fails `go test`, as do
stale entries and tool renames. The template catalog is chained the same way:
`template_list` ↔ `build.KnownTemplates` ↔ the actual template dispatch
switch, each link guarded by a test.

## Tools

### VM lifecycle

| Tool | Kind | Description |
|------|------|-------------|
| `vm_list` | read-only | All VMs with template, resources, and run state |
| `vm_status` | read-only | Detailed status: boot phase, install state, ports, uptime, persisted health checks |
| `template_list` | read-only | All templates with defaults and per-template `vm_create` parameters |
| `vm_create` | mutating | Create a VM from any template — full CLI flag parity (disks, GPU, NIC, virtiofs, SSH, per-template extras); `reinstall=true` wipes and recreates |
| `vm_start` | mutating | Boot detached; waits for readiness by default; reports first-boot install passes |
| `vm_stop` | mutating | Graceful powerdown; `force=true` kills the process |
| `vm_delete` | destructive | Delete a stopped VM and its disks |
| `vm_move` | mutating | Move the boot disk to another directory, stopping/restarting as needed |
| `vm_autostart` | mutating | Get or set daemon autostart |
| `vm_config_get` | read-only | Full persisted VM configuration as JSON |

### Inspection & guest access

| Tool | Kind | Description |
|------|------|-------------|
| `vm_exec` | mutating | Run a shell command in the guest via the guest agent; returns stdout/stderr/exit code |
| `vm_ip` | read-only | Resolve the guest's primary IP |
| `vm_ports` | read-only | Listening TCP ports + process names inside the guest |
| `vm_services` | read-only | Declared services (HTTP/HTTPS/SPICE/TCP) with host-reachable URLs |
| `vm_logs` | read-only | Tail of the process log; `journal=true` for the guest systemd journal, `kernel=true` for kernel messages |
| `vm_check` | mutating | Run the guest's health-check script now |
| `vm_stats` | read-only | One CPU/memory/disk/network sample via QMP |
| `vm_qmp` | mutating | Raw QMP command passthrough (daemon-routed when applicable) |
| `vm_display` | read-only | How to connect to the VM's display (VNC/SPICE/Moonlight) with reachability probe |

### Host services & assets

| Tool | Kind | Description |
|------|------|-------------|
| `image_catalog` | read-only | Pullable distros and their known versions |
| `image_pull` | mutating | Download/build a base image into the cache (long-running for large images) |
| `vm_backup` | mutating | rsync guest directories to the host over SSH; `dirs` are explicit guest paths |
| `vm_backup_list` | read-only | Past and in-progress backup runs |
| `gpu_list` | read-only | PCI devices by IOMMU group with drivers (Linux) |
| `gpu_status` | read-only | VFIO passthrough pre-flight for one device |
| `runner_key` | mutating | A GitHub runner's SSH public key (generates the global key on first use) |
| `runner_snapshot` | mutating | Persist a runner VM's registration credentials (encrypted) for token-free reinstalls |
| `mirror_status` / `mirror_start` / `mirror_stop` / `mirror_purge` | mixed | Host-side pacman caching proxy (Linux) |

All tools return structured JSON, so agents get machine-readable fields
rather than formatted terminal output.

## CLI-only exemptions

These stay on the CLI, each declared with its reason in the coverage map:

- `vee ssh` (interactive shell — `vm_exec` covers one-off commands), `vee config` editing (validated TUI form — `vm_config_get` covers reads), `vee view` client launching (`vm_display` returns the connection details), tunnel *opening* (`vm_services` reports endpoints), `vee monitor`'s live TUI (`vm_stats` samples once), log/serial *following* (tools return bounded tails).
- `vee daemon` install/uninstall and `gpu bind`/`gpu unbind` (root, host-mutating).
- `vee dashboard` (long-lived web server) and `vee ssh-share` (long-lived vsock proxy).

## Template parameters over MCP

Everything the CLI collects via prompts is a tool parameter instead:

| Template | Parameters |
|----------|-----------|
| `passthrough` | `nvme_dev` + `ovmf_vars` (the CLI wizard's inputs) |
| `github-runner` | `runner_url`, `runner_token` (not needed when a credential snapshot exists), `runner_labels`, `runner_ssh_key`; a newly generated SSH key is returned as `runner_public_key` |
| `torrent` | `share_mounts` (host/guest dir pairs), and `nordvpn_token`/`nordvpn_country` or `wireguard_conf` for the VPN kill-switch |
| `jellyfin` | `media` (same spec syntax as `--media`), `media_secrets`; missing secret keys are reported in the error so the agent can retry |
| `macos` | `ipsw` (latest/URL/path), `macosvm_dir`, `skip_first_boot` |

### First-boot semantics

Cloud-init templates run an install pass on first boot and may power the VM
off when it finishes. `vm_start` reports this as `status: "install-complete"`;
calling `vm_start` again boots the installed system. The first `vm_create` of
a template may also download a base image, which can take minutes — agents
should use a generous timeout for that call (or pre-fetch with `image_pull`).

### Guest command execution

`vm_exec` talks to the QEMU guest agent, so it works before SSH is configured
and regardless of the NIC mode, but it requires a guest-agent-enabled template
(most Linux templates are). macOS guests on Virtualization.framework have no
guest agent — use `vee ssh` for those. Commands are bounded by
`timeout_seconds` (default 60); a timed-out command may still be running in
the guest.

## Protocol hygiene

Stdout carries the MCP JSON-RPC stream. vee's logs go to
`~/.vee/logs/vee.log` as usual; passing `--verbose` raises them to debug level
and streams them to stderr, which is protocol-safe, and download progress bars
render on stderr. Nothing in the tool call paths writes to stdout.
