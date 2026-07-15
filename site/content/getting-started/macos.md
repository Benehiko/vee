---
title: macOS host
weight: 40
---

vee runs on Apple Silicon Macs using Hypervisor.framework (**HVF**) with aarch64 guests and accelerated virtio-gpu.

## Requirements

- Apple Silicon (arm64). Intel Macs may work under TCG but are untested.
- A `qemu-system-aarch64` on `PATH` (Homebrew QEMU works for basic use).

## Guest architecture

HVF only accelerates guests whose architecture matches the host, so guests must be **aarch64**. x86 guests fall back to slow TCG software emulation.

vee uses the `virt` machine type, edk2 ARM (AAVMF) firmware, and a windowed `cocoa` display.

## What works vs what degrades

| Feature | Status on macOS |
|---------|-----------------|
| Boot / lifecycle / cloud-init | Works (built-in ISO writer, or `hdiutil` fallback — no `xorriso` needed) |
| Accelerated virtio-gpu (virgl) | Works with a virgl-capable QEMU; software (llvmpipe) otherwise |
| Networking | User-mode NAT only |
| VFIO GPU passthrough | Unavailable (Linux-only) — warns and degrades |
| virtiofs shares | Unavailable |
| vsock SSH-agent sharing | Unavailable |
| swtpm TPM | Unavailable |
| Bridge networking | Unavailable |

vee warns and falls back rather than emitting broken QEMU arguments.

## Guest images

Ubuntu and Fedora cloud images ship arm64 builds and work out of the box. The `ubuntu-server` live ISO, Arch/`gaming-arch`, Bazzite, Alpine/`docker`, TrueNAS, and Windows templates are x86-only and not wired for arm64.

## Accelerated graphics — current limitation

The accelerated `vee-qemu` bundle for macOS is **not currently buildable**: QEMU 10.0.2 (pinned for apple-gfx) and the only macOS-patched virglrenderer (a 2021-era fork around QEMU 6.2) do not compile together, so no `darwin-arm64` QEMU asset is published yet. Venus/Vulkan and apple-gfx (macOS-guest Metal graphics) are experimental.

The QEMU binary vee uses is code-signed with the `com.apple.security.hypervisor` entitlement (vee applies an ad-hoc signature automatically).

See [docs/macos.md](https://github.com/Benehiko/vee/blob/main/docs/macos.md) for the full per-guest GPU matrix and setup details.
