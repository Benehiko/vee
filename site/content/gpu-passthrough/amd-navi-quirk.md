---
title: AMD Navi ROM BAR Quirk
weight: 20
---

AMD RDNA 2 and RDNA 3 GPUs (RX 6000 and RX 7000 series) have a known issue when
used with `vfio-pci`: the ROM BAR returns an invalid signature (`0xffff` instead
of `0xaa55`). This causes a 65-second PCIe bus reset hang and leaves the device
stuck in the `D3cold` power state.

## Symptoms

```
vfio-pci 0000:08:00.0: Invalid PCI ROM header signature: expecting 0xaa55, got 0xffff
error getting device from group 22: No such device
```

QEMU exits immediately after this message. The GPU cannot be used until the host
is cold-rebooted.

## Fix: supply a VBIOS dump

**Do not use `rombar=1` without also supplying a VBIOS file.** vee defaults to
`rombar=0` to avoid this hang. To re-enable ROM BAR (some games need it), you
must supply the correct VBIOS dump via `rom_file`.

### Get the VBIOS

If `vfio-pci` owns the GPU at boot (the typical setup), `amdgpu` never loads
and the sysfs ROM interface is unavailable. Use one of these sources:

1. **TechPowerUp VGABIOS database** — download the ROM for your exact board
   revision from [techpowerup.com/vgabios](https://www.techpowerup.com/vgabios/).
   This is the easiest option.

2. **Sysfs dump** — only possible if a second GPU drives the host display
   (so `amdgpu` never touches the passthrough card):

   ```sh
   echo 1 | sudo tee /sys/bus/pci/devices/0000:08:00.0/rom
   sudo cat /sys/bus/pci/devices/0000:08:00.0/rom > ~/.vee/gpu.rom
   echo 0 | sudo tee /sys/bus/pci/devices/0000:08:00.0/rom
   ```

### Configure vm.yaml

```yaml
gpu:
  mode: passthrough
  pci_addr: "0000:08:00.0"
  rom_file: "/home/youruser/.vee/gpu.rom"
```

With `rom_file` set, QEMU serves the VBIOS from the file instead of probing
the BAR, and the 65-second hang is avoided.

## D3cold recovery

If the GPU is already stuck in `D3cold` from a previous failed start, vee will
attempt a PCI function-level reset before the next `vee start`. If that fails,
a cold reboot of the host is required to recover the device.
