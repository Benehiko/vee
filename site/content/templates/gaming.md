---
title: gaming
weight: 50
---

GPU passthrough gaming VM. Creates a fresh VM with a new OS disk, 16G RAM, 6 CPUs, and `anti_detect` enabled for anti-cheat compatibility.

## Prerequisites

See [GPU Passthrough Prerequisites]({{< relref "/gpu-passthrough/prerequisites" >}}).

## Create

```sh
vee create win-gaming --template gaming --gpu-pci 08:00.0
```

## Defaults

| Setting | Value |
|---------|-------|
| Memory | 16G |
| CPUs | 6 (3 cores × 2 threads) |
| Network | Bridge (br0) |
| GPU | VFIO passthrough |
| Anti-detect | Enabled |
| UEFI | Yes |

## Anti-detect

The `anti_detect: true` flag configures QEMU to hide virtualization artifacts that anti-cheat software (Easy Anti-Cheat, BattlEye) may detect. This includes hiding the QEMU vendor string from CPUID and the PCI subsystem IDs.

## Notes

For a gaming VM booting from an existing NVMe (e.g. a Windows install you already use on bare metal), use the [`passthrough` template]({{< relref "passthrough" >}}) instead.
