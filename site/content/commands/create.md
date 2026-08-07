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
| `--disk` | Attach an additional blank disk of this size alongside the OS disk, e.g. `40G`. On the `macos` template it sets the size of the guest's own disk instead. |
| `--distro` | Linux distro for templates that support it |
| `--data-disk` | Extra raw disk in `path:label` format (repeatable) |
| `--nvme-dev` | Pass an NVMe device through directly (passthrough template) |
| `--ipsw` | macos template: restore image — `latest`, an https URL, or a local `.ipsw` |
| `--macosvm-dir` | macos template: import an existing macosvm bundle instead of restoring |
| `--skip-first-boot` | macos template: skip offline provisioning (guest boots into Setup Assistant) |
| `--ovmf-vars` | Custom OVMF_VARS.fd for UEFI |
| `--gpu-pci` | GPU PCI address for passthrough, e.g. `08:00.0` |
| `--nic-mode` | Networking mode: `user` or `bridge` |
| `--virtiofs-dir` | Host directory to share into the VM |
| `--nested` | Expose nested virtualization (EL2) so the guest can run KVM — arm64 QEMU guests only; under HVF needs QEMU ≥ 11.1 plus an M3+ Mac on macOS 15+ |

## Extra disks

`--disk <size>` attaches one additional qcow2 disk to the guest, created under
`~/.vee/vms/<name>/storage/`. It is blank: the guest sees an unpartitioned
block device (typically `/dev/vdb`) and you partition, format and mount it
yourself. The OS disk keeps the first slot, so boot order is unaffected.

To grow the OS disk instead of adding a second one, set `default_disk_size` in
`~/.vee/config.yaml` before creating the VM.

`--data-disk` is a different thing: it passes an existing *host* block device
through to the guest, rather than creating a new image.

## Examples

```sh
# Default Ubuntu 24.04 server
vee create myvm

# Developer VM with Docker
vee create dev --template devbox

# Developer VM with a second, blank 60G disk to format in the guest
vee create dev --template devbox --disk 60G

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
