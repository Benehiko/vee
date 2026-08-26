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
