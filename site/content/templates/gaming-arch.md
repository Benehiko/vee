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
| `--gpu-gl-backend` | *(host default)* | Host OpenGL backend for `--gpu-mode=virtio`: `on`, `es`, or `core` |
| `--gpu-venus` | `false` | Vulkan-over-virtio on the virtio-gpu-gl device (experimental) |
| `--gpu-hostmem` | `8G` | Host memory window for Venus blob resources (requires `--gpu-venus`) |
| `--pointer` | `mouse` | Guest pointing device for `--gpu-mode=virtio`: `mouse` (relative, for mouse-look in games — the gaming default) or `tablet` (absolute, the desktop default elsewhere) |
| `--nic-mode` | `bridge` | `user` for NAT instead of a bridge |
| `--headless` | `false` | No local display window |
| `--virtiofs-dir` | `""` | Host directory shared into the guest (tag `Games`) |

## GPU vendor

`--gpu-vendor` selects the in-guest driver:

- `amd` (default) — `mesa` + `vulkan-radeon` (plus `vulkan-virtio` when not using passthrough)
- `nvidia` — `nvidia` + `nvidia-utils`, `nvidia-persistenced` enabled
- `virtio` — AMD base stack for virtio-gpu

## Virtio GPU acceleration

Without passthrough, the guest renders through virtio-gpu against the host's
GPU: QEMU opens a windowed display with a host GL context and virglrenderer
forwards guest rendering onto it. On Linux this is `virtio-vga-gl` with
`-display gtk,gl=on`; the host windowing system (Wayland or X11) makes no
difference.

`--gpu-gl-backend` overrides the host GL backend — `on` for the Linux EGL
stack, `es` (ANGLE onto Metal, stable) or `core` (native, unstable) on macOS.
Leave it unset to take the host default.

`--gpu-venus` adds the Vulkan-over-virtio (Venus) path on top, so guest Vulkan
reaches the host GPU instead of falling back to software. It is experimental:
it needs a QEMU and virglrenderer built with Venus, a host Vulkan driver, and a
guest carrying Mesa's `vulkan-virtio` ICD — which `gaming-arch` installs for
non-passthrough VMs. Desktop Vulkan compositing is still unreliable; prefer
plain virgl OpenGL for the desktop session.

`--gpu-hostmem` sizes the host memory window Venus uses for blob resources, the
shared buffers that carry Vulkan allocations between host and guest. It
defaults to `8G`. Raise it for high render resolutions or heavy texture loads —
it tracks the GPU working set, not guest RAM, so changing `--memory` does not
change what this should be.

All three apply only to `--gpu-mode=virtio` (and `--gpu-hostmem` only with
`--gpu-venus`); combining them with `passthrough` or `none` is rejected rather
than quietly ignored.

### Guest-side settings on virtio

The installer writes three files into the guest that only make sense when
virtio-gpu is the sole GPU; a passthrough install gets none of them:

- `/etc/udev/rules.d/90-vee-virtio-render.rules` keeps the virtio render node
  readable by the `render` group.
- `/etc/environment.d/99-vee-venus-icd.conf` pins the Vulkan loader to Mesa's
  virtio (Venus) ICD, so RADV does not grab the node and fail.
- `/etc/environment.d/98-vee-kwin-udmabuf.conf` sets
  `KWIN_DISABLE_UDMABUF_IMPORT=1`. KWin 6.7 and later wrap client `wl_shm`
  buffers (cursor images, software-rendered windows) in udmabuf dma-bufs and
  import them into EGL. On virtio-gpu the kernel turns such an import into a
  guest-memory blob resource that the host virgl renderer cannot use as a
  texture; the first time kwin samples one, virglrenderer raises a fatal
  `Illegal resource` context error and the compositor stops rendering while
  the guest stays alive over SSH. Hovering a Steam or Chromium window, or any
  Xwayland client, is enough to trigger it. With the import disabled kwin
  uploads shm buffers through `glTexImage2D` as it did before 6.7.

If a VM created before this setting existed freezes its display when Steam
or a browser opens, add the same line to `/etc/environment.d/` in the guest
and reboot it.

### Pointer device

`--pointer` picks the virtio pointing device next to the virtio keyboard.
Gaming VMs default to `mouse`; every other template defaults to `tablet`.
`tablet` is an absolute pointer: the host cursor maps straight
onto the guest screen and the QEMU window never grabs it, which is what you
want for a desktop. Wayland compositors (kwin included) deliver absolute
motion with a zero relative delta, though, and a game that locks the pointer
for mouse-look reads only relative deltas, so the camera never moves.
`--pointer mouse` attaches a relative `virtio-mouse-pci` instead: the QEMU
window grabs the cursor on click and forwards deltas, and Ctrl+Alt+G releases
the grab. On Linux this also switches the VM window from QEMU's GTK display
to its SDL display, because GTK3 cannot lock or warp the pointer on a
Wayland host and its grab never captures motion; SDL2 locks the pointer
natively on Wayland and X11. It also boots the x86 board with `vmport=off`:
otherwise the guest's `psmouse` driver finds VMware's vmmouse behind QEMU's
backdoor port, QEMU makes that absolute device its current mouse, and host
motion turns absolute again. Change it on an existing VM with
`vee config <vm> --pointer mouse`; QEMU cannot swap input devices live, so it
applies on the next start.

## Streaming & tuning

The guest installs Sunshine (HTTPS on port `47991`) for Moonlight streaming, plus gaming performance tuning: real-time priority / memlock limits, hugepages, a low-latency PipeWire quantum, SDDM autologin, and a serial console. A self-verifying `vee-check` health script is installed for [`vee check`]({{< relref "/commands/check" >}}).

See [Gaming Setup]({{< relref "/gpu-passthrough/gaming-setup" >}}) for the full Sunshine + Moonlight walkthrough.
