---
title: windows
weight: 80
---

Windows VM with UEFI Secure Boot and TPM 2.0 emulation via `swtpm`.

vee builds the Windows install ISO automatically — you do **not** need to supply
your own. See [vee pull → Windows ISOs](../../commands/pull/#windows-isos) for how
the ISO is assembled.

## Prerequisites

- `swtpm` installed on the host
- `nerdctl` or `docker` on `PATH` (used to build the Windows ISO in a container)

## Create

```sh
vee create mywindows --template windows
```

The first create for a given Windows version resolves the build via UUP dump,
downloads the ESD from Microsoft, and assembles a bootable UEFI ISO. The result is
cached under `~/.vee/iso/`, so later VMs reuse it. Pre-fetch a specific version with:

```sh
vee pull windows win11        # or win10, server2025, server2022
```

## Defaults

| Setting | Value |
|---------|-------|
| Memory | 24G |
| CPUs | 4 |
| UEFI | Yes (Secure Boot) |
| TPM | 2.0 (swtpm) |
| Display | SPICE |

The template also attaches the VirtIO driver ISO and WinFSP so the guest gets
paravirtualized disk, network, and virtiofs support.

## Notes

- Use `vee view mywindows` to open the SPICE console during Windows setup.
- `swtpm` is started automatically when the VM boots and stopped when it shuts down.
- vee downloads Windows bits from Microsoft's servers and assembles the ISO locally
  — it never redistributes Windows. You still need a valid Windows license key.
