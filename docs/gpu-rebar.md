# Boot-Time Resizable BAR for Passthrough GPUs

```
[ boot ] ── PCI enumeration ── vee-rebar early hook ── vfio_pci binds ── [ VM start ]
                                     │
                          resize BAR0 (e.g. 256M → 16G)
```

`vee gpu rebar` installs an initramfs early hook that resizes a GPU's PCI
BAR on every boot, before any driver binds to the device.

## Why this exists

A GPU that is bound to `vfio-pci` at boot keeps its firmware-default BAR0
size — typically 256M. The native driver (e.g. `amdgpu`) would resize the
BAR to cover the whole VRAM at probe time, but a VFIO device is never
probed by it. QEMU exposes only the current host BAR size to the guest, so
the guest is stuck with the small BAR too.

For gaming guests this costs some performance (no ReBAR/SAM). For compute
stacks it can be fatal: tinygrad's AMD backend, for example, maps all VRAM
CPU-visible and fails allocations beyond the 256M host-visible window with
errors like:

```
MemoryError: Allocation of 16.00 MB failed on AMD. Used: 0 B
```

## Why at boot and not at runtime

A PCI BAR can only be resized while **no driver is bound**. The obvious
runtime fix — unbind from `vfio-pci`, resize, rebind — does not work on
cards with reset quirks: RDNA3 GPUs (RX 7000 series) frequently do not
survive a runtime unbind/rebind cycle and need a cold boot afterwards.

The only universally safe point is early userspace: PCI is enumerated,
sysfs is mounted, and no driver has loaded yet. mkinitcpio runs early
hooks *before* it loads the `MODULES` array (where `vfio_pci` and its
`ids=` option live), so the hook wins the race by construction.

## Usage

```sh
# Show current and supported BAR sizes
vee gpu rebar 08:00.0

# Install a boot-time resize of BAR0 to 16G (prompts for sudo)
vee gpu rebar 08:00.0 --size 16G

# A different BAR index
vee gpu rebar 08:00.0 --bar 2 --size 256M

# Remove the resize again
vee gpu rebar 08:00.0 --remove
```

`--size` must be a power of two and supported by the device;
`vee gpu rebar <addr>` lists the supported sizes (read from the device's
ReBAR capability via sysfs).

After installing, **reboot** to apply. For GPUs with reset quirks, use a
cold boot (full power-off), not a warm reboot.

## What gets installed

| Path | Purpose |
|------|---------|
| `/etc/vee/rebar.conf` | One resize per line: `<pci-addr> <bar> <size-exponent>` |
| `/etc/initcpio/hooks/vee-rebar` | Early hook that applies the conf at boot |
| `/etc/initcpio/install/vee-rebar` | mkinitcpio build script (embeds hook + conf) |
| `/etc/mkinitcpio.conf` | `vee-rebar` added to `HOOKS=(...)` after `udev` |

The initramfs is regenerated (`mkinitcpio -P`) on every install/remove.
Removing the last entry deletes all three files and the `HOOKS` entry.

The size exponent follows the ReBAR capability encoding: size = 2^exp MB
(so 8 = 256M, 14 = 16G).

## Host requirements

- **BIOS**: Above 4G Decoding / Resizable BAR support enabled. If the
  host firmware never enabled large address windows, the kernel may fail
  to reallocate the bridge windows and the hook logs
  `vee-rebar: resize <addr> failed` on the console (boot continues with
  the old size).
- **mkinitcpio** (Arch Linux and derivatives). Other initramfs systems
  (dracut, initramfs-tools) are not yet supported — on those, replicate
  the hook manually in an equivalent early-boot phase.

## Verifying after reboot

```sh
vee gpu rebar 08:00.0          # BAR0 current size: 16G
lspci -vv -s 08:00.0 | grep 'Region 0'
```

Inside a guest with the device passed through, the BAR appears at the new
size and `amdgpu` reports the full VRAM as CPU-visible
(`mem_info_vis_vram_total`).
