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

## DirectX with Helios (experimental)

On x86_64 Linux hosts, `--gpu-mode=helios` gives the guest hardware-accelerated
Direct3D 11/12, Vulkan, OpenGL and OpenCL on the host GPU without GPU
passthrough. It uses the [WinBoat Helios](https://github.com/winboat-org/helios)
WDDM driver over virtio-gpu + Venus. Helios is pre-release, and D3D12 is limited
(feature level 11_0 natively). Use it for game testing where passthrough is not
an option, not as a stand-in for real hardware.

```sh
vee create wintest --template windows --distro-version win11 \
  --gpu-mode helios \
  --helios-setup ./HeliosSetup.exe \
  --qemu-binary ~/src/helios/qemu-helios/build-helios/qemu-system-x86_64 \
  --qemu-env LD_LIBRARY_PATH=$R/lib \
  --qemu-env RENDER_SERVER_EXEC_PATH=$R/libexec/virgl_render_server
vee start wintest
vee helios status wintest --wait
vee helios verify wintest
vee view wintest        # VNC
```

Compared to the table above, the VM runs with Secure Boot **off** (Helios is
test-signed), 16G / 8 CPUs, and an egl-headless display exported over VNC
instead of SPICE. It needs Helios' QEMU and virglrenderer forks and its
`HeliosSetup.exe`, which vee installs unattended at first logon and then reboots
through provisioning. Host setup, the in-guest flow, running tests in the
desktop session, and troubleshooting are in
[docs/windows-helios.md](https://github.com/Benehiko/vee/blob/main/docs/windows-helios.md).
See also [vee helios](../../commands/helios/).

## Notes

- x86_64: use `vee view mywindows` to open the SPICE console during Windows
  setup, and `swtpm` is started automatically when the VM boots and stopped
  when it shuts down.
- arm64: the install renders in the QEMU window on the host (ramfb); use RDP
  once the guest is up for a resizable desktop.
- vee downloads Windows bits from Microsoft's servers and assembles the ISO locally
  — it never redistributes Windows. You still need a valid Windows license key.
