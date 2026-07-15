---
title: gaming-arch
weight: 50
---

Arch Linux + KDE Plasma (Wayland) + Steam gaming VM. Boots the Arch ISO, runs a full in-guest install to a new disk, and installs Sunshine for game streaming. Runs with virtio-gpu by default, or GPU passthrough when you pass a PCI address.

## Prerequisites

For passthrough, see [GPU Passthrough Prerequisites]({{< relref "/gpu-passthrough/prerequisites" >}}).

## Create

```sh
# virtio-gpu (no passthrough)
vee create arch-gaming --template gaming-arch

# GPU passthrough
vee create arch-gaming --template gaming-arch \
  --gpu-mode passthrough --gpu-pci 08:00.0
```

## Defaults

| Setting | Value |
|---------|-------|
| Memory | 16G |
| CPUs | 8 |
| Disk | 80G qcow2 |
| Network | Bridge (br0) — or `--nic-mode user` |
| Display | SPICE + virtio-gpu (passthrough swaps to the physical GPU) |
| Guest agent | Enabled |
| UEFI | Yes |
| User | `gamer` (password = username unless overridden) |

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--user` | `gamer` | Guest login username |
| `--password` | *(= username)* | Guest login password |
| `--gpu-mode` | `none`/`virtio` | `passthrough` to hand a physical GPU to the guest |
| `--gpu-pci` | `""` | PCI address for passthrough (e.g. `08:00.0`) |
| `--gpu-vendor` | `amd` | Guest GPU driver: `amd`, `nvidia`, or `virtio` |
| `--nic-mode` | `bridge` | `user` for NAT instead of a bridge |
| `--headless` | `false` | No local display window |
| `--virtiofs-dir` | `""` | Host directory shared into the guest (tag `Games`) |

## GPU vendor

`--gpu-vendor` selects the in-guest driver:

- `amd` (default) — `mesa` + `vulkan-radeon` (plus `vulkan-virtio` when not using passthrough)
- `nvidia` — `nvidia` + `nvidia-utils`, `nvidia-persistenced` enabled
- `virtio` — AMD base stack for virtio-gpu

## Streaming & tuning

The guest installs Sunshine (HTTPS on port `47991`) for Moonlight streaming, plus gaming performance tuning: real-time priority / memlock limits, hugepages, a low-latency PipeWire quantum, SDDM autologin, and a serial console. A self-verifying `vee-check` health script is installed for [`vee check`]({{< relref "/commands/check" >}}).

See [Gaming Setup]({{< relref "/gpu-passthrough/gaming-setup" >}}) for the full Sunshine + Moonlight walkthrough.
