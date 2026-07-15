---
title: Windows host
weight: 45
---

vee runs on Windows (amd64) using the Windows Hypervisor Platform (**WHPX**) with x86-64 guests.

## Requirements

- Enable the **Windows Hypervisor Platform** and **Hyper-V** optional features, plus firmware virtualization. Without them, vee falls back to slow `-accel tcg`.
- A `qemu-system-x86_64.exe` on `PATH` built with `--enable-whpx`. vee does not yet publish a `windows-amd64` QEMU bundle.

## Guest architecture

WHPX accelerates **x86-64** guests. vee uses the `q35` machine type.

## What works vs what degrades

| Feature | Status on Windows |
|---------|-------------------|
| Boot / lifecycle / WHPX acceleration | Works |
| QMP / guest-agent | Works over loopback TCP (`127.0.0.1:<port>`) instead of UNIX sockets |
| cloud-init (NoCloud seed) | Works (built-in pure-Go ISO9660/Joliet writer) |
| Serial / SPICE console | Works |
| VFIO GPU passthrough | Unavailable (Linux-only) |
| virtiofs shares | Unavailable |
| vsock SSH-agent sharing | Unavailable |
| Bridge networking | Unavailable — user-mode NAT instead |
| CPU pinning | Unavailable (needs `taskset`/`/proc`) |
| swtpm TPM | Unavailable |
| Daemon service installer | Unavailable — `vee daemon install` is Linux-only |

## Process differences

- `vee ssh` spawns `ssh` as a child process and waits (there is no `execve` on Windows).
- Interactive commands register Ctrl-C only (no `SIGTERM`).
- Graceful stop is QMP `system_powerdown` followed by a hard terminate.

## Key limitation — no nested virtualization

A Windows host that is **itself a VM** cannot hardware-accelerate guests under WHPX. The inner guest fails with `c0350005` / `Unexpected VP exit code 4` because the outer hypervisor (e.g. KVM) does not expose nested APIC virtualization. There is no vee-side fix — run on bare-metal Windows.

See [docs/windows.md](https://github.com/Benehiko/vee/blob/main/docs/windows.md) for the full feature matrix and [docs/windows-guests.md](https://github.com/Benehiko/vee/blob/main/docs/windows-guests.md) for the Windows-guest ISO pipeline.
