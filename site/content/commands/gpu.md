---
title: vee gpu
weight: 110
---

Manage GPU VFIO passthrough bindings and run preflight checks.

```
vee gpu <subcommand>
```

## Subcommands

### vee gpu list

List all PCI GPUs on the host with their IOMMU group, current driver, and power state.

```sh
vee gpu list
```

### vee gpu bind

Bind a GPU (and optionally its audio peer) to `vfio-pci`.

```sh
sudo vee gpu bind 08:00.0
```

All devices in the same IOMMU group must be bound together or QEMU cannot take ownership of the group. `vee gpu bind` warns if there are unbound peers.

### vee gpu unbind

Unbind a device from `vfio-pci` and restore the original driver.

```sh
sudo vee gpu unbind 08:00.0
```

### vee gpu status

Run a full preflight check for a GPU before starting a passthrough VM.

```sh
vee gpu status 08:00.0 --memory 16G
```

Checks:

| Check | Pass condition |
|-------|----------------|
| Driver | `vfio-pci` |
| IOMMU group device | `/dev/vfio/<group>` exists |
| Group isolation | All peer devices also bound to `vfio-pci` |
| Locked memory | `memlock` ≥ requested `--memory` |
| Power state | Not `D3cold` (or recoverable) |
