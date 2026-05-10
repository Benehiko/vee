---
title: passthrough
weight: 60
---

GPU passthrough VM booting directly from an existing NVMe or block device. No new OS disk is created — the VM boots from your existing installation.

## Prerequisites

See [GPU Passthrough Prerequisites]({{< relref "/gpu-passthrough/prerequisites" >}}).

## Create

```sh
vee create linux-gaming --template passthrough \
  --nvme-dev /dev/disk/by-id/nvme-CT2000P3PSSD8_... \
  --ovmf-vars /path/to/OVMF_VARS.fd \
  --gpu-pci 08:00.0
```

`--nvme-dev` passes the NVMe device directly through to the VM as a virtio block device. The device is not copied — the VM reads and writes directly to the physical drive.

`--ovmf-vars` specifies a pre-enrolled OVMF_VARS.fd from a previous installation. This preserves Secure Boot keys and EFI boot entries.

## Notes

- This template does not use cloud-init. Set `ssh_user` in `vm.yaml` to your existing user on the drive.
- The NVMe device is passed through as a raw block device. Ensure the host OS does not have it mounted.
- For AMD GPU passthrough, see [AMD Navi ROM BAR quirk]({{< relref "/gpu-passthrough/amd-navi-quirk" >}}).
