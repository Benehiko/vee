---
title: docker
weight: 27
---

Lightweight Alpine Linux VM running the Docker daemon, exposed on `tcp://localhost:2375` (no TLS, local only) via a user-mode port forward. Point a local Docker client at it without touching the host Docker install.

## Create

```sh
vee create dockerhost --template docker
```

## Defaults

| Setting | Value |
|---------|-------|
| Memory | 2G |
| CPUs | 2 |
| Disk | 10G qcow2 (COW overlay on Alpine image) |
| Network | User-mode NAT, `127.0.0.1:2375` → guest `2375` |
| Display | None (headless) |
| UEFI | No (BIOS cloud image) |

## Use it

```sh
export DOCKER_HOST=tcp://localhost:2375
docker ps
docker run --rm hello-world
```

> The daemon listens on `tcp://0.0.0.0:2375` **inside** the guest, but only `127.0.0.1:2375` on the host is forwarded — so it is reachable from localhost only. There is no TLS or auth; do not expose port 2375 beyond localhost.
