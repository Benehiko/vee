---
title: desktop
weight: 25
---

Graphical Linux desktop with accelerated virtio-gpu (virgl). Boots a distro cloud image and cloud-init installs a minimal GNOME + Mesa GL/Vulkan stack with GDM autologin. Works on Apple Silicon (aarch64).

## Create

```sh
vee create workstation --template desktop --distro fedora
```

## Defaults

| Setting | Value |
|---------|-------|
| Memory | 8G |
| CPUs | 4 |
| Disk | 20G qcow2 (COW overlay on cloud image) |
| Network | User-mode NAT |
| Display | virtio-gpu (GL) |
| UEFI | Yes |

## Distros

`--distro` selects the base OS:

| Distro | Notes |
|--------|-------|
| `fedora` | Default. `@base-x` + `gnome-shell`/`gnome-session`/`nautilus`/`gnome-control-center` + GDM, Wayland autologin |
| `ubuntu` | `ubuntu-desktop-minimal` + `gdm3`, Wayland autologin |

Other distros are rejected.

## GPU acceleration

The desktop renders through `virtio-gpu-gl`. On Linux hosts this uses the host GPU via virgl; on macOS it uses the Cocoa GL backend (see [macOS host]({{< relref "/getting-started/macos" >}})). A virgl-capable QEMU is required for hardware acceleration — otherwise rendering falls back to software (llvmpipe).
