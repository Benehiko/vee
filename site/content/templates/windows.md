---
title: windows
weight: 80
---

Windows VM with a fully unattended install. On x86_64 hosts it boots with UEFI
Secure Boot and TPM 2.0 emulation via `swtpm`; on arm64 hosts (Apple Silicon)
it boots the arm64 media on the `virt` board with the Windows 11 hardware
checks bypassed.

vee builds the Windows install ISO automatically — you do **not** need to supply
your own. See [vee pull → Windows ISOs](../../commands/pull/#windows-isos) for how
the ISO is assembled; the media matches the host architecture.

## Prerequisites

- `nerdctl` or `docker` on `PATH` (used to build the Windows ISO in a container)
- `swtpm` installed on the host — x86_64 only (arm64 guests attach no TPM;
  Windows ARM64 cannot initialize QEMU's TPM device, so the answer file
  bypasses the check instead)

## Create

```sh
vee create mywindows --template windows
```

The first create for a given Windows version resolves the build via UUP dump,
downloads the ESD from Microsoft, and assembles a bootable UEFI ISO. The result is
cached under `~/.vee/iso/`, so later VMs reuse it. Pre-fetch a specific version with:

```sh
vee pull windows win11        # both arches
vee pull windows win10        # both arches
vee pull windows server2025   # x86_64 hosts only (no arm64 Server media)
vee pull windows server2022   # x86_64 hosts only
```

## Defaults

| Setting | x86_64 hosts | arm64 hosts (Apple Silicon) |
|---------|--------------|------------------------------|
| Memory | 8G | 8G |
| CPUs | 4 | 4 |
| UEFI | Yes (Secure Boot) | Yes (no Secure Boot variant; checks bypassed) |
| TPM | 2.0 (swtpm) | None — Windows ARM64 cannot bind QEMU's TPM; checks bypassed |
| System disk | virtio (viostor injected) | NVMe (inbox driver) |
| Install media | IDE CD-ROM | USB mass storage |
| Display | SPICE | ramfb in the QEMU window; RDP for a desktop |
| Default version | `win10` | `win11` (`win10` available; no arm64 Server media) |

The template attaches the virtio-win driver ISO on both arches. On x86_64 it
also bundles WinFSP, so the guest gets paravirtualized disk, network, **and**
virtiofs support out of the box. On arm64 the guest gets virtio networking
(NetKVM's attestation-signed ARM64 build is injected during install) and the
NVMe system disk needs no driver at all — but virtiofs shares are not yet
supported: virtio-win ships no ARM64 guest-tools installer and its ARM64
`viofs` driver is test-signed only
([virtio-win#1337](https://github.com/virtio-win/kvm-guest-drivers-windows/issues/1337)).
OpenSSH is still enabled at first logon on both arches.

## Notes

- x86_64: use `vee view mywindows` to open the SPICE console during Windows
  setup, and `swtpm` is started automatically when the VM boots and stopped
  when it shuts down.
- arm64: the install renders in the QEMU window on the host (ramfb); use RDP
  once the guest is up for a resizable desktop.
- vee downloads Windows bits from Microsoft's servers and assembles the ISO locally
  — it never redistributes Windows. You still need a valid Windows license key.
