---
title: Templates
weight: 30
---

vee ships with built-in templates that configure QEMU arguments, cloud-init, networking, and disk layout for common VM types. Pass one with `--template`; `ubuntu-server` is the default.

| Template | Description |
|----------|-------------|
| [`ubuntu-server`]({{< relref "ubuntu-server" >}}) | Ubuntu 24.04 LTS, UEFI, user-mode NIC (default) |
| [`server`]({{< relref "server" >}}) | openssh + ufw + fail2ban, `--distro` ubuntu/arch/fedora |
| [`desktop`]({{< relref "desktop" >}}) | GNOME + Mesa, accelerated virtio-gpu, `--distro` fedora/ubuntu |
| [`devbox`]({{< relref "devbox" >}}) | Docker + zsh, `--distro` ubuntu/arch/fedora |
| [`docker`]({{< relref "docker" >}}) | Alpine Linux, Docker daemon on `tcp://localhost:2375` |
| [`gaming-arch`]({{< relref "gaming-arch" >}}) | Arch + KDE Plasma + Steam, virgl or GPU passthrough |
| [`gaming-bazzite`]({{< relref "gaming-bazzite" >}}) | Bazzite (Fedora Atomic) gaming ISO, KDE Plasma |
| [`gaming`]({{< relref "gaming" >}}) | Legacy alias for `gaming-arch` |
| [`passthrough`]({{< relref "passthrough" >}}) | Raw NVMe boot + GPU passthrough, SPICE, virtiofs |
| [`truenas`]({{< relref "truenas" >}}) | TrueNAS SCALE, AHCI OS disk, bridge NIC, SPICE |
| [`torrent`]({{< relref "torrent" >}}) | Lightweight qbittorrent-nox, optional VPN kill-switch |
| [`jellyfin`]({{< relref "jellyfin" >}}) | Jellyfin, NFS/SMB/host-dir/block/USB media, mDNS |
| [`windows`]({{< relref "windows" >}}) | Windows, UEFI secure boot, TPM 2.0 |
| [`github-runner`]({{< relref "github-runner" >}}) | Self-hosted Actions runner, outbound HTTPS |

## Overriding defaults

Each template sets its own memory/CPU/disk defaults. Override any of them per VM:

```sh
vee create big --template devbox --memory 16G --cpus 8 --disk 100G
```

## vm.yaml

Every VM's configuration is stored in `~/.vee/vms/<name>/vm.yaml`. You can edit it directly (or with [`vee config`]({{< relref "/commands/config" >}})) to change any setting after creation — see [vm.yaml]({{< relref "/advanced/vm-yaml" >}}).

```yaml
name: myvm
template: ubuntu-server
memory: 2G
cpus: 2
ssh_user: ubuntu
guest_agent: true
```
