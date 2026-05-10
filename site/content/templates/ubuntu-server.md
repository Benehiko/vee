---
title: ubuntu-server
weight: 10
---

The default template. Creates an Ubuntu 24.04 LTS server VM with UEFI boot and user-mode networking.

## Defaults

| Setting | Value |
|---------|-------|
| OS | Ubuntu 24.04 LTS |
| Memory | 2G |
| CPUs | 2 |
| Disk | 20G qcow2 |
| Network | User-mode NAT |
| UEFI | Yes |
| SSH key | Injected via cloud-init |

## Create

```sh
vee create myvm
vee create myvm --template ubuntu-server --memory 4G --cpus 4
```

## Notes

- Cloud-init runs on first boot to set hostname, create the default user, and inject the vee SSH public key.
- Default user is `ubuntu`. Use `vee ssh myvm` — no password needed.
- Supports `--virtiofs-dir` to share a host directory into the VM.
