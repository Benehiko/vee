---
title: gaming-bazzite
weight: 52
---

[Bazzite](https://bazzite.gg/) (immutable Fedora Atomic) gaming ISO with Steam + Proton and KDE Plasma pre-installed. Boots the ISO directly and uses Bazzite's own installer — there is no cloud-init and no SSH key injection.

## Prerequisites

For passthrough, see [GPU Passthrough Prerequisites]({{< relref "/gpu-passthrough/prerequisites" >}}).

## Create

```sh
# virtio-gpu (no passthrough)
vee create bazzite --template gaming-bazzite

# GPU passthrough
vee create bazzite --template gaming-bazzite \
  --gpu-mode passthrough --gpu-pci 08:00.0
```

## Defaults

| Setting | Value |
|---------|-------|
| Memory | 16G |
| CPUs | 8 |
| Disk | 80G qcow2 |
| Network | Bridge (br0) |
| Display | virtio-gpu — passthrough swaps to the physical GPU + SPICE |
| Guest agent | Enabled |
| UEFI | Yes |

## Notes

- Because the install is driven by Bazzite's own installer, this template does **not** accept `--user` or SSH keys. Complete first-boot setup at the console (or SPICE).
- `--gpu-vendor` (default `amd`) only affects passthrough driver selection.

For an SSH-managed, cloud-init gaming VM instead, use [`gaming-arch`]({{< relref "gaming-arch" >}}).
