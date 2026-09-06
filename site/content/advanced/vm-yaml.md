---
title: vm.yaml Reference
weight: 10
---

Every VM's full configuration is stored in `~/.vee/vms/<name>/vm.yaml`. You can edit this file directly — changes take effect on the next `vee start`.

## Full example

```yaml
name: linux-gaming
template: passthrough
memory: 16G
cpus: 6
sockets: 1
cores: 3
threads: 2
cpu_model: host

disks:
  - path: /dev/disk/by-id/nvme-CT2000P3PSSD8_...
    size: ""
    format: raw
    interface: virtio
    media: disk
    cache: none
    readonly: false
    passthrough: true

nic:
  mode: bridge
  bridge: br0
  model: virtio-net-pci
  mac: 52:54:54:8d:72:76

gpu:
  mode: passthrough
  pci_addr: "0000:08:00.0"
  extra_vfio_addrs:
    - "0000:08:00.1"
  rom_file: /home/user/.vee/gpu.rom
  anti_detect: true

uefi:
  enabled: true
  vars_path: /home/user/.vee/vms/linux-gaming/OVMF_VARS.fd

spice:
  port: 5930
  disable_ticketing: true

ssh_user: youruser
guest_agent: true

extra_devices:
  - virtio-gpu-pci,edid=on,xres=1920,yres=1080

vga: none

created_at: 2026-01-01T00:00:00Z
```

## Fields

### Top-level

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | VM name (matches directory name) |
| `template` | string | Template used to create the VM |
| `backend` | string | Virtualization backend: `qemu` (default when empty) or `vz` (Apple Virtualization.framework for macOS guests on Apple Silicon hosts — experimental, requires a `macos:` section and the `vee-vz-helper` binary; [#51](https://github.com/Benehiko/vee/issues/51)) |
| `arch` | string | Guest CPU architecture in QEMU naming (`aarch64`, `x86_64`). Empty = host-native (hardware-accelerated). A cross-arch value runs the guest under TCG emulation — much slower — resolving the matching system `qemu-system-<arch>` binary, firmware, and machine type automatically. QEMU backend only. Set by the `omarchy` template (x86_64-only ISO) |
| `memory` | string | RAM, e.g. `16G` |
| `cpus` | int | Total vCPU count |
| `sockets` | int | CPU socket topology |
| `cores` | int | Cores per socket |
| `threads` | int | Threads per core (SMT) |
| `cpu_model` | string | QEMU CPU model, e.g. `host` |
| `nested` | bool | Expose nested virtualization (EL2) to the guest so it can run KVM. arm64 (aarch64) QEMU guests only; under HVF needs QEMU ≥ 11.1 and an M3+ Mac on macOS 15+ |
| `ssh_user` | string | Default SSH user for `vee ssh` |
| `guest_agent` | bool | Enable QGA virtio-serial socket |
| `vga` | string | VGA device type; set to `none` for passthrough |
| `extra_devices` | []string | Additional `-device` arguments passed to QEMU |
| `created_at` | timestamp | Creation time (set automatically) |

### macos

Only for `backend: vz` guests (see [macOS guests](../../getting-started/macos-guests/)). Written by the `macos` template; the blobs are opaque Virtualization.framework values, base64 in JSON and `!!binary` in YAML — the same encoding `macosvm.json` uses.

| Field | Type | Description |
|-------|------|-------------|
| `auxiliary_storage` | string | Absolute path to the guest's auxiliary storage (the NVRAM analog) |
| `hardware_model` | binary | Hardware model the guest was restored for |
| `machine_identifier` | binary | Machine identity bound to the installed guest |
| `min_cpus` | int | CPU floor from the restore image; vee clamps up to it |
| `min_memory_bytes` | int | Memory floor from the restore image; vee clamps up to it |
| `display_width_px` / `display_height_px` / `display_ppi` | int | Guest display size (defaults 1920x1200 @ 80 ppi) |

### disks[]

| Field | Description |
|-------|-------------|
| `path` | Device path or image file path |
| `size` | Image size (empty for raw block passthrough) |
| `format` | `qcow2` or `raw` |
| `interface` | `virtio`, `ide`, `ahci`, `nvme` (inbox driver on Windows ARM64), `usb` (mass storage on the VM's USB controller) |
| `media` | `disk` or `cdrom` |
| `cache` | QEMU cache mode (`none`, `writeback`, etc.) |
| `readonly` | Mount read-only |
| `passthrough` | `true` for raw block device passthrough |

### nic

| Field | Description |
|-------|-------------|
| `mode` | `user` (NAT) or `bridge` |
| `bridge` | Host bridge interface name (bridge mode) |
| `model` | QEMU NIC model (`virtio-net-pci`, `e1000`, etc.) |
| `mac` | MAC address (assign a stable one for bridge VMs) |

### gpu

| Field | Description |
|-------|-------------|
| `mode` | `none`, `virtio` (accelerated virtio-gpu), or `passthrough` (VFIO, Linux host only). `apple-gfx` is rejected: QEMU's macOS-guest path is unusable upstream ([#50](https://github.com/Benehiko/vee/issues/50)) — run macOS guests on `backend: vz` instead |
| `pci_addr` | Primary GPU PCI address, e.g. `0000:08:00.0` (passthrough) |
| `extra_vfio_addrs` | Additional IOMMU group peer addresses (passthrough) |
| `rom_file` | Path to VBIOS dump (required for AMD Navi) |
| `anti_detect` | Hide virtualization artifacts from anti-cheat |
| `gl_backend` | Host GL backend for `virtio` mode: `es` (ANGLE/Metal, macOS default, stable), `core` (native, unstable), `on` (Linux EGL). Empty picks the host default. |
| `venus` | Enable Vulkan-over-virtio (Venus) on the virtio-gpu-gl device. Experimental; needs a Venus-capable QEMU and a host Vulkan driver. The guest needs the Mesa `vulkan-virtio` ICD, so this is a Linux-guest path. |
| `host_mem` | Host memory window for Venus blob resources, e.g. `8G` (only with `venus: true`; defaults to `8G` when Venus is on and this is unset). Size it against the GPU working set — render resolution, texture sizes, buffers in flight — not against guest RAM. |
| `pointer` | Guest pointing device for `virtio` mode: `tablet` (absolute; the host cursor maps onto the guest, no grab — the default) or `mouse` (relative; the QEMU window grabs the cursor and forwards deltas, which pointer-locked games need for mouse-look). On Linux `mouse` also uses QEMU's SDL window instead of GTK, since GTK cannot lock the pointer on Wayland. Applies on the next start. |

`gl_backend`, `venus` and `host_mem` are only read when `mode: virtio`, and
`host_mem` only alongside `venus: true`. Setting them anywhere else is rejected
at create time rather than silently ignored. All three are also available on
`vee create` as `--gpu-gl-backend`, `--gpu-venus` and `--gpu-hostmem`.

On a macOS host, `mode: virtio` emits `virtio-gpu-gl-pci` with `-display cocoa,gl=es`;
`mode: passthrough` is rejected (VFIO is Linux-only). See [docs/macos.md](../../../docs/macos.md).

### uefi

| Field | Description |
|-------|-------------|
| `enabled` | Enable UEFI boot (OVMF) |
| `vars_path` | Path to OVMF_VARS.fd (mutable EFI variables store) |

### spice

| Field | Description |
|-------|-------------|
| `port` | Host port for the SPICE display server |
| `disable_ticketing` | Allow unauthenticated SPICE connections |
