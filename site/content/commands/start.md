---
title: vee start
weight: 20
---

Start a VM in the background and wait until it is ready.

```
vee start <name>
```

vee spawns QEMU as a background process, then polls until the VM is reachable via SSH or the guest agent confirms it is up. Progress is shown as a spinner; the command exits when the VM is ready.

If stdout is not a TTY (e.g. in a script), the spinner is skipped and a plain message is printed when the VM is ready.

## GPU passthrough VMs

For VMs with GPU passthrough, `vee start` runs a pre-flight check before launching:

1. Reads the GPU's PCIe power state and driver binding.
2. If the device is in `D3cold` (left over from an unclean exit), attempts a PCI function-level reset via sysfs.
3. Verifies the IOMMU group is fully bound to `vfio-pci`.

If the preflight fails and the device cannot be recovered, a cold reboot of the host is required.

## Example

```sh
vee start linux-gaming
```
