---
title: vee tunnel
weight: 60
---

Connect to a service exposed by a running VM. HTTP/HTTPS services open in the
default browser, SPICE prints a `spice://` URL, and plain TCP services print the
forwarded address.

```
vee tunnel <name> [service]
```

Without a service argument, lists the services the VM exposes and how to reach
each one.

## Example

```sh
vee tunnel torrents
# SERVICE           PROTOCOL    CONNECTION
# ────────────────────────────────────────────────────────────
# serial            file        serial console log (follow)
# qbittorrent       http        http://localhost:<proxy> → guest:8080

vee tunnel torrents qbittorrent
# tunnelling localhost:44859 → torrents:8080
# http://localhost:44859
```

The forwarded local port is printed and held open until you press Ctrl+C.

## How the connection is made

`vee tunnel` picks a path based on how the VM is networked:

- **User-mode NAT VMs** with a matching host forward already have the port on
  the host, so it is used directly.
- **Bridge-mode VMs** get a direct TCP proxy to the guest's address — no SSH
  involved.
- **Guests that block the port from the LAN** — most often a VPN kill-switch,
  as in the `torrent` and `bitmagnet` templates — fall back to an SSH tunnel.
  Such a guest drops every LAN port except 22, so the service port is probed
  first and the tunnel is run over SSH when it is unreachable.

The SSH fallback prefers a recorded loopback port when something is really
listening there (QEMU's host forward, or the daemon's bridge proxy), and
otherwise connects to the guest's own address on port 22 — the same dispatch
`vee ssh` uses.

## Serial console

`serial` is a built-in pseudo-service backed by the VM's `serial.log`. It works
whether or not the VM is running:

```sh
vee tunnel myvm serial
```

Pass `--raw` to stream serial output without stripping ANSI escape codes.

## Binding to the LAN

Tunnels bind `localhost` by default. `--host` binds `0.0.0.0` instead, so other
devices on the network can reach the service:

```sh
vee tunnel media jellyfin --host
```

vee adds no authentication — the guest application's own login is the only
protection — so the command warns and asks for confirmation before binding.
Pass `--yes` to skip the prompt in scripts.

## Background tunnels

`--background` hands the tunnel to the vee daemon instead of holding it in the
foreground. The daemon keeps it up for as long as the VM runs and re-establishes
it automatically after a host reboot.

```sh
vee tunnel media jellyfin --host --background   # publish and persist
vee tunnel --list                               # show all background tunnels
vee tunnel media jellyfin --stop                # remove one
```

An HTTP service published this way is also routed by name on port 80, as
`http://<service>.<hostname>/` — for example `http://jellyfin.benehiko-desktop/`.
Use `--hostname` to publish under a different label.

See [background tunnels](../../advanced/tunnels/) for the routing details, the
DNS setup the names need, and how the registry persists across reboots.
