---
title: Templates
weight: 30
---

vee ships with built-in templates that configure QEMU arguments, cloud-init, networking, and disk layout for common VM types.

| Template | Description |
|----------|-------------|
| [`ubuntu-server`]({{< relref "ubuntu-server" >}}) | Ubuntu 24.04 LTS, UEFI, user-mode NIC |
| [`devbox`]({{< relref "devbox" >}}) | Docker + zsh via cloud-init |
| [`server`]({{< relref "server" >}}) | openssh + ufw + fail2ban |
| [`truenas`]({{< relref "truenas" >}}) | TrueNAS SCALE, bridge NIC, SPICE display |
| [`gaming`]({{< relref "gaming" >}}) | GPU passthrough, 16G RAM, anti-detect |
| [`passthrough`]({{< relref "passthrough" >}}) | GPU passthrough booting from existing disk |
| [`torrent`]({{< relref "torrent" >}}) | Lightweight, qbittorrent-nox |
| [`jellyfin`]({{< relref "jellyfin" >}}) | Jellyfin media server with NFS/SMB/host-dir/block/USB libraries and mDNS |
| [`windows`]({{< relref "windows" >}}) | Windows, UEFI secboot, TPM 2.0 |

## vm.yaml

Every VM's configuration is stored in `~/.vee/vms/<name>/vm.yaml`. You can edit this file directly to change any setting after creation.

```yaml
name: myvm
template: ubuntu-server
memory: 2G
cpus: 2
ssh_user: ubuntu
guest_agent: true
```
