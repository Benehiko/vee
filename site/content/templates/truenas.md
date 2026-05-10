---
title: truenas
weight: 40
---

TrueNAS SCALE VM with bridge networking, SPICE display, and raw block device passthrough for ZFS data drives.

## Prerequisites

- Bridge interface configured on the host (see [Networking]({{< relref "/getting-started/networking" >}}))
- User in `disk` group (`sudo usermod -aG disk $USER`)

## Create

```sh
vee create mynas --template truenas \
  --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0S3H6:EXOS22TB-A \
  --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0WD9J:EXOS22TB-B
```

`--data-disk` format: `device-path:label`. Each disk is passed through as a raw block device. The label appears in the TrueNAS UI.

## Defaults

| Setting | Value |
|---------|-------|
| Memory | 8G |
| CPUs | 4 |
| OS disk | AHCI SATA (required by TrueNAS installer) |
| Network | Bridge (br0) |
| Display | SPICE |
| UEFI | Yes |

## Accessing the UI

During first boot, use `vee view mynas` to open the SPICE console and complete the TrueNAS installer. After installation, the TrueNAS web UI is available at the VM's LAN IP on port 80/443.
