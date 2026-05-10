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
| `memory` | string | RAM, e.g. `16G` |
| `cpus` | int | Total vCPU count |
| `sockets` | int | CPU socket topology |
| `cores` | int | Cores per socket |
| `threads` | int | Threads per core (SMT) |
| `cpu_model` | string | QEMU CPU model, e.g. `host` |
| `ssh_user` | string | Default SSH user for `vee ssh` |
| `guest_agent` | bool | Enable QGA virtio-serial socket |
| `vga` | string | VGA device type; set to `none` for passthrough |
| `extra_devices` | []string | Additional `-device` arguments passed to QEMU |
| `created_at` | timestamp | Creation time (set automatically) |

### disks[]

| Field | Description |
|-------|-------------|
| `path` | Device path or image file path |
| `size` | Image size (empty for raw block passthrough) |
| `format` | `qcow2` or `raw` |
| `interface` | `virtio`, `sata`, `nvme` |
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
| `mode` | `passthrough` |
| `pci_addr` | Primary GPU PCI address, e.g. `0000:08:00.0` |
| `extra_vfio_addrs` | Additional IOMMU group peer addresses |
| `rom_file` | Path to VBIOS dump (required for AMD Navi) |
| `anti_detect` | Hide virtualization artifacts from anti-cheat |

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
