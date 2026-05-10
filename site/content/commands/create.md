---
title: vee create
weight: 10
---

Create a new VM from a template.

```
vee create <name> [flags]
```

## Flags

| Flag | Description |
|------|-------------|
| `--template` | VM template to use (default: `ubuntu-server`) |
| `--memory` | RAM, e.g. `4G` (default: template-specific) |
| `--cpus` | Number of vCPUs (default: template-specific) |
| `--disk-size` | OS disk size, e.g. `40G` |
| `--distro` | Linux distro for templates that support it |
| `--data-disk` | Extra raw disk in `path:label` format (repeatable) |
| `--nvme-dev` | Pass an NVMe device through directly (passthrough template) |
| `--ovmf-vars` | Custom OVMF_VARS.fd for UEFI |
| `--gpu-pci` | GPU PCI address for passthrough, e.g. `08:00.0` |
| `--nic-mode` | Networking mode: `user` or `bridge` |
| `--virtiofs-dir` | Host directory to share into the VM |

## Examples

```sh
# Default Ubuntu 24.04 server
vee create myvm

# Developer VM with Docker
vee create dev --template devbox

# TrueNAS with data drives
vee create mynas --template truenas \
  --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0S3H6:EXOS22TB-A \
  --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0WD9J:EXOS22TB-B

# GPU passthrough booting from existing NVMe
vee create linux-gaming --template passthrough \
  --nvme-dev /dev/disk/by-id/nvme-... \
  --ovmf-vars /path/to/OVMF_VARS.fd \
  --gpu-pci 08:00.0
```
