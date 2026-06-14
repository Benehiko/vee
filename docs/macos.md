# macOS (Apple Silicon) support

vee can run QEMU virtual machines on an Apple Silicon Mac using the
**Hypervisor.framework (HVF)** accelerator. This document covers how the macOS
host port works, how to get GPU acceleration inside the guest, and the honest
limitations of each path.

> **Scope.** Apple Silicon (arm64) hosts are the supported target. Intel Macs may
> work but are not actively tested. VFIO GPU passthrough, virtiofs, vhost-vsock,
> swtpm, and bridge networking are Linux-host features and are unavailable on
> macOS — vee degrades gracefully (warns and falls back) rather than emitting
> QEMU arguments that cannot work.

## How vee adapts to a macOS host

vee derives host-specific defaults from `internal/platform`:

| Concern | Linux | macOS (Apple Silicon) |
|---------|-------|-----------------------|
| Accelerator | `-accel kvm` | `-accel hvf` |
| Native guest arch | `x86_64` | `aarch64` |
| Machine type | `q35` | `virt` |
| QEMU binary | `qemu-system-x86_64` | `qemu-system-aarch64` |
| Firmware | OVMF (`/usr/share/OVMF`) | edk2 ARM (AAVMF) |
| Windowed display | `gtk` | `cocoa` |
| Networking | bridge or user-mode | user-mode NAT (bridge unavailable) |

For acceleration to work, the guest architecture must match the host: on Apple
Silicon, run **aarch64 guests** under HVF. x86 guests fall back to TCG software
emulation (very slow), where GPU acceleration is moot.

## The QEMU binary (important)

**Stock/Homebrew QEMU on macOS renders in software (llvmpipe) only** — it is not
built with virglrenderer, so `gpu.mode=virtio` will *not* be hardware accelerated
with it. Accelerated virtio-gpu on macOS requires a QEMU built with
virglrenderer + ANGLE (and MoltenVK for Vulkan/Venus).

vee resolves the QEMU binary in this order (`internal/qemubin`):

1. A published `vee-qemu` release asset for `darwin-arm64` (virgl-capable), if available.
2. A drop-in at `~/.vee/bin/qemu-system-aarch64`.
3. Homebrew (`/opt/homebrew/bin/qemu-system-aarch64`) or `PATH`.

If none is found, vee prints guidance to install QEMU. For **GPU acceleration**,
install a virgl-capable build — for example a `qemu-virgl` Homebrew tap (such as
`knazarov/qemu-virgl`), or use the QEMU bundled with [UTM](https://mac.getutm.app/),
which ships the patched virglrenderer + ANGLE (+ MoltenVK) stack.

### The vee-qemu bundle

vee's own `darwin-arm64` asset is a self-contained bundle (`bin/`, `lib/`,
`share/`) built by [`scripts/build-qemu-macos.sh`](../scripts/build-qemu-macos.sh)
and published by the `Build and release vee-qemu` GitHub Actions workflow on an
Apple Silicon runner. It bundles the virglrenderer + ANGLE (GLES→Metal) +
MoltenVK dylibs and the edk2 ARM firmware so it runs without any system
dependencies. The `lib/` dylibs are reached via the binary's `@loader_path/../lib`
rpath, and QEMU finds its datadir at `../share/qemu`.

The binary must be code-signed with the `com.apple.security.hypervisor`
entitlement to use HVF. vee handles this automatically: on install it strips the
download quarantine and (re-)applies an ad-hoc signature with the entitlement
(`internal/qemubin/qemu-entitlements.plist`) — macOS honors the hypervisor
entitlement for ad-hoc signatures, so no Apple Developer certificate is needed.
Homebrew/UTM binaries are already signed.

## GPU acceleration: what works per guest

| Guest | Path | Status |
|-------|------|--------|
| **Linux (arm64)** | `gpu.mode: virtio` → `virtio-gpu-gl-pci` + `-display cocoa,gl=es` | ✅ OpenGL (virgl) stable; Vulkan (Venus) experimental |
| **macOS** | `gpu.mode: apple-gfx` (ParavirtualizedGraphics.framework) | ⚠️ Building blocks present; full template wiring pending |
| **Windows (arm64)** | 2D only (`virtio-gpu-pci`) + RDP | ❌ No virtio-gpu 3D driver exists for Windows; VFIO unavailable on macOS |

### Linux guest (the main use case)

```yaml
gpu:
  mode: virtio
  gl_backend: es      # ANGLE/Metal (stable). "core" = native OpenGL (unstable)
  # venus: true       # opt-in Vulkan over virtio (experimental)
  # host_mem: 8G      # host memory window for Venus blob resources
```

vee emits `virtio-gpu-gl-pci` with `-display cocoa,gl=es`. In the guest, install
recent Mesa; `glxinfo -B` / `eglinfo` should report a `virgl` renderer (not
`llvmpipe`). Venus (Vulkan) is opt-in and young — desktop Vulkan compositing is
unreliable, so prefer virgl OpenGL for the desktop and reserve Venus for explicit
Vulkan/compute apps.

Headless or SPICE VMs fall back to a plain (2D) `virtio-gpu-pci`, since there is
no windowed GL context.

### macOS guest (apple-gfx / PVG)

apple-gfx uses Apple's `ParavirtualizedGraphics.framework` for Metal-accelerated
graphics and requires QEMU ≥ 10.0, the `vmapple` machine, AVPBooter firmware from
the host `Virtualization.framework`, and a binary signed with both
`com.apple.security.hypervisor` and `com.apple.security.virtualization`. It
accelerates **macOS guests only** (single display, no live migration; macOS 12.x
guests on the vmapple path). The device building blocks exist
(`qemu.AppleGFXDevice`, `qemu.VMAppleMachineType`); end-to-end template wiring is
in progress.

### Windows guest

There is no production virtio-gpu 3D driver for Windows, and VFIO passthrough (the
only real route to Windows GPU acceleration) is a Linux-host feature. On macOS,
Windows-on-ARM guests get unaccelerated 2D graphics; use RDP for a usable desktop.

## Guest images on Apple Silicon

Guests must be **aarch64** to run accelerated under HVF. Image availability is
distro-specific, so vee selects the arm64 image where one exists and refuses
clearly otherwise:

| Distro / template | arm64 on Apple Silicon |
|-------------------|------------------------|
| **Ubuntu** (cloud image: `server`, `devbox --distro ubuntu`, `jellyfin`, `runner`, `torrent`) | ✅ arm64 cloud image |
| `ubuntu-server` (live-server ISO) | ❌ x86-only ISO — use a cloud-image Ubuntu template |
| Arch / `gaming-arch` | ❌ official ISO is x86-only |
| Bazzite / `gaming-bazzite` | ❌ x86-only |
| Fedora, Alpine / `docker` | ❌ not yet wired for arm64 (planned) |
| TrueNAS | ❌ x86-only |
| Windows | ❌ no ARM image pipeline; no GPU 3D on macOS regardless |

For a GPU-accelerated Linux desktop on Apple Silicon, use an **Ubuntu arm64**
guest (cloud image) with `gpu.mode: virtio`.

## Limitations summary

- No VFIO GPU passthrough (Linux-host kernel feature).
- No virtiofs shares, vhost-vsock SSH-share, swtpm TPM, or bridge networking.
- x86 guests run under slow TCG emulation; use aarch64 guests.
- Accelerated `gpu.mode: virtio` needs a virgl-capable QEMU; stock QEMU = software GL.
- Venus/Vulkan and apple-gfx are experimental.
