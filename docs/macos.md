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

## Host prerequisites

Install this with Homebrew before creating a VM:

```sh
brew install qemu      # qemu-system-aarch64 + edk2 ARM firmware (HVF-enabled)
```

**No ISO tooling is required.** Every cloud-init template (`server`, `devbox`,
`desktop`, `jellyfin`, `github-runner`, …) needs a NoCloud seed ISO
(`cidata.iso`). vee builds it with `xorriso` (preferred) or `genisoimage` when
one is on `PATH`, and otherwise falls back to **`hdiutil`**, which ships with
macOS — so a stock Mac needs no extra package. If you already have `xorriso`
installed (`brew install xorriso`) it is used automatically; there is no
functional difference in the resulting seed. For **GPU-accelerated** display you
additionally need a virgl-capable QEMU — see "The QEMU binary" below; stock
Homebrew QEMU renders in software.

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

#### Building vee-qemu locally

You don't need to wait for a published release — you can build the bundle on your
own Apple Silicon Mac and have vee use it immediately. The build script signs the
binary with the hypervisor entitlement itself, and `INSTALL_LOCAL=1` extracts the
result into `~/.vee` (which vee's resolver prefers over Homebrew):

```sh
QEMU_VERSION=10.0.2 INSTALL_LOCAL=1 ./scripts/build-qemu-macos.sh
```

This produces `~/.vee/bin/qemu-system-aarch64` plus its bundled `lib/` and
`share/` — no GitHub release, no checksum, no `version.go` edit. vee picks it up
on the next `vee start`. (Without `INSTALL_LOCAL`, the script just leaves the
`dist/*.tar.gz` asset for publishing.)

The load-bearing, hard-to-test step is the virglrenderer + ANGLE build (the
`knazarov/qemu-virgl` Homebrew tap). If that tap is unavailable the script falls
back to a plain virglrenderer with **no macOS GL acceleration** and warns — so
check the build log for that warning if the guest reports an `llvmpipe` renderer.

#### Known limitations of the virgl bundle

The accelerated bundle is **not yet buildable from a stock toolchain** as of this
writing — the pinned QEMU and the only macOS-patched virglrenderer are from
different eras and do not compile together. This is the biggest open item in the
macOS port; the notes below are for whoever picks it up.

- **QEMU ↔ virglrenderer version mismatch (the blocker).** The build pins
  **QEMU 10.0.2** (needed for `apple-gfx` / `ParavirtualizedGraphics`), but the
  only macOS-patched virglrenderer with the ANGLE (GLES→Metal) stack is the
  `knazarov/qemu-virgl` tap's `virglrenderer 20211212.1` — a **December 2021**
  build (~QEMU 6.2 era). QEMU 10.2's `hw/display/virtio-gpu-virgl.c` calls
  `virgl_renderer_resource_get_info()` / `struct virgl_renderer_resource_info`,
  which do not exist in that renderer, so the build fails to compile. The tap's
  own `qemu-virgl` formula sidesteps this by building QEMU from a matching 2021
  git revision. Resolving it requires one of: a newer macOS-patched
  virglrenderer, a QEMU-side shim for the old renderer API, or dropping the QEMU
  version (which loses `apple-gfx`). Until then there is **no published
  `darwin-arm64` asset** and `Checksums["darwin-arm64"]` in
  `internal/qemubin/version.go` stays empty.

- **Toolchain fix-ups the script now handles (but which pin it to a moving
  target).** On current Homebrew the dependency step needs: the tap formula named
  `libepoxy-angle` (not `libepoxy`); `brew trust knazarov/qemu-virgl` (Homebrew ≥
  6 refuses untrusted taps); a Python `distlib` for QEMU's `configure` venv; and
  the ANGLE/epoxy/virgl include+lib dirs exported via `CPATH`/`LIBRARY_PATH`
  (QEMU 10.x does not thread `--extra-cflags` to its `ui/egl-*.c` objects). These
  are environment-sensitive and may drift again as Homebrew and the tap change.

- **No acceleration without the bundle.** When no virgl-capable QEMU is resolved,
  vee runs on stock/Homebrew QEMU, where `gpu.mode: virtio` renders in **software
  (llvmpipe)** — the VM is fully usable but the desktop is not GPU-accelerated. A
  guest that reports an `llvmpipe` (not `virgl`) renderer is on this path.

- **Venus/Vulkan is doubly experimental here** — it needs both a working virgl
  bundle *and* MoltenVK, and desktop Vulkan compositing is unreliable; prefer
  virgl OpenGL.

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
| **Ubuntu** (cloud image: `server`, `devbox --distro ubuntu`, `desktop --distro ubuntu`, `jellyfin`, `runner`, `torrent`) | ✅ arm64 cloud image |
| **Fedora** (Cloud Base qcow2: `server --distro fedora`, `devbox --distro fedora`, `desktop`) | ✅ arm64 cloud image |
| `ubuntu-server` (live-server ISO) | ❌ x86-only ISO — use a cloud-image Ubuntu template |
| Arch / `gaming-arch` | ❌ official ISO is x86-only |
| Bazzite / `gaming-bazzite` | ❌ x86-only |
| Alpine / `docker` | ❌ not yet wired for arm64 (planned) |
| TrueNAS | ❌ x86-only |
| Windows | ❌ no ARM image pipeline; no GPU 3D on macOS regardless |

### GPU-accelerated desktop (the `desktop` template)

For a graphical, GPU-accelerated Linux desktop on Apple Silicon, use the
`desktop` template — it boots the distro's arm64 cloud image, installs a minimal
GNOME desktop plus the Mesa GL/Vulkan drivers via cloud-init, and runs with
`gpu.mode: virtio` (→ `virtio-gpu-gl-pci` + Cocoa window):

```sh
vee create box --template desktop                 # Fedora (default)
vee create box --template desktop --distro ubuntu # Ubuntu arm64
```

Acceleration requires a virgl-capable QEMU (see "The vee-qemu bundle" below);
with stock/Homebrew QEMU the desktop still renders, but in software (llvmpipe).

## Limitations summary

- No VFIO GPU passthrough (Linux-host kernel feature).
- No virtiofs shares, vhost-vsock SSH-share, swtpm TPM, or bridge networking.
- x86 guests run under slow TCG emulation; use aarch64 guests.
- Accelerated `gpu.mode: virtio` needs a virgl-capable QEMU; stock QEMU = software GL.
- The accelerated **vee-qemu bundle is not currently buildable** (QEMU 10.x vs the
  2021-era macOS virglrenderer); no `darwin-arm64` asset is published yet. See
  "Known limitations of the virgl bundle" above.
- Venus/Vulkan and apple-gfx are experimental.
