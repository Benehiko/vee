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
| Memory | 6G |
| CPUs | 2 (1 socket, 1 core, 2 threads) |
| OS disk | AHCI SATA (required by TrueNAS installer) |
| Data disks | virtio-blk-pci, one dedicated iothread each |
| Network | Bridge (br0) |
| Display | SPICE |
| UEFI | Yes |

ZFS and NFS are both multi-threaded and throughput-sensitive: `nfsd` runs a
thread pool, and ZFS performs checksumming, compression and write aggregation
away from the calling thread. A single vCPU serializes all of that, so NFS
clients can see writes queue for seconds even when the pool itself retires them
in milliseconds. The default of 2 vCPUs — exposed as a single hyperthreaded
core — keeps `nfsd` off the critical path without taking a second physical core
away from the host.

The 6G default is sized from measurement rather than convention. On a 4G VM,
ARC held 2.1G against a 2.9G cap at a 95% hit rate, with both `arc_no_grow` and
`memory_throttle_count` at zero — ARC was working well and simply short of
headroom, not starved. 6G lifts the default ARC cap to roughly 3G and leaves
about 1G free for `nfsd` once it is no longer single-threaded. Raise it further
only if `arc_no_grow` starts reporting non-zero under load.

Each passthrough data drive is given its own QEMU iothread. Without one, every
virtio-blk device is serviced on the main QEMU loop, where disk I/O competes
with vCPU execution — the dominant source of latency on a storage VM fronting
several spinning drives.

## Accessing the UI

During first boot, use `vee view mynas` to open the SPICE console and complete the TrueNAS installer. After installation, the TrueNAS web UI is available at the VM's LAN IP on port 80/443.
