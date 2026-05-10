---
title: torrent
weight: 70
---

Lightweight VM running qbittorrent-nox (headless). Suitable for a dedicated download VM on a NAS or home server.

## Create

```sh
vee create dl --template torrent
```

## Defaults

| Setting | Value |
|---------|-------|
| Memory | 1G |
| CPUs | 2 |
| Network | User-mode NAT |

## Access

The qbittorrent-nox web UI is available at the VM's IP on port 8080. Use `vee tunnel dl 8080` to forward it to localhost.
