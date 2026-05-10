---
title: vee logs
weight: 80
---

Stream the QEMU console output for a VM.

```
vee logs <name>
```

Tails the QEMU output log file for the named VM. Useful for diagnosing boot failures, kernel panics, and VFIO errors.

## GPU passthrough

For GPU passthrough VMs, VFIO errors appear here early in the boot sequence. Common messages to look for:

```
vfio-pci 0000:08:00.0: Invalid PCI ROM header signature: expecting 0xaa55, got 0xffff
```

→ Supply a VBIOS dump via `rom_file` in `vm.yaml`. See [GPU Passthrough]({{< relref "/gpu-passthrough/" >}}).
