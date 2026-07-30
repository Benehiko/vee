# macOS (Apple Silicon) support

This document covers both of vee's macOS stories, which are unrelated to each
other:

1. **macOS as a host** — running QEMU virtual machines on an Apple Silicon Mac
   through the **Hypervisor.framework (HVF)** accelerator. How the host port
   works, how to get GPU acceleration inside the guest, and the honest
   limitations of each path. That is everything up to "macOS guests" below.
2. **macOS as a guest** — running a macOS VM on Apple's
   **Virtualization.framework**, a second backend with its own helper binary.
   See [macOS guests (Virtualization.framework)](#macos-guests-virtualizationframework).

> **Scope.** Both stories need an Apple Silicon (arm64) host; macOS guests
> require one, since Apple only permits macOS VMs on Apple hardware. Intel Macs may
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
installed (`brew install xorriso`) it is used automatically.

### xorriso vs. hdiutil: what differs

The two builders produce slightly different ISO9660 images, but both are valid
NoCloud seeds and cloud-init consumes them identically (verified end-to-end on a
Fedora guest under HVF):

| | `xorriso` / `genisoimage` | `hdiutil makehybrid` |
|---|---|---|
| Command | `-as mkisofs -joliet -rock …` | `-iso -joliet -default-volume-name cidata …` |
| Volume label | `cidata` | `cidata` (matched case-insensitively) |
| Long filenames | Joliet **and** Rock Ridge | Joliet only |
| POSIX perms/ownership in the image | Rock Ridge preserves them | not recorded |

The only real difference is that hdiutil omits the **Rock Ridge** extension. That
does not affect the seed: the guest kernel reads the lowercase `user-data` /
`meta-data` names from the **Joliet** descriptor regardless, and cloud-init reads
those files' *contents* — it never relies on their on-ISO permissions or
ownership (the guest mounts the read-only seed itself). So the resulting seed is
functionally equivalent for provisioning; the choice of builder is purely about
which tool happens to be installed.

For **GPU-accelerated** display you additionally need a virgl-capable QEMU — see
"The QEMU binary" below; stock Homebrew QEMU renders in software.

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

The **virgl-accelerated** variant of the bundle is not buildable from a stock
toolchain as of this writing — the pinned QEMU and the only macOS-patched
virglrenderer are from different eras and do not compile together. The published
bundle is therefore the plain-HVF build. Restoring GL acceleration is the biggest
open item in the macOS port; the notes below are for whoever picks it up.

- **QEMU ↔ virglrenderer version mismatch (the blocker).** The build pins
  **QEMU 10.0.2**, but the
  only macOS-patched virglrenderer with the ANGLE (GLES→Metal) stack is the
  `knazarov/qemu-virgl` tap's `virglrenderer 20211212.1` — a **December 2021**
  build (~QEMU 6.2 era). QEMU 10.2's `hw/display/virtio-gpu-virgl.c` calls
  `virgl_renderer_resource_get_info()` / `struct virgl_renderer_resource_info`,
  which do not exist in that renderer, so the build fails to compile. The tap's
  own `qemu-virgl` formula sidesteps this by building QEMU from a matching 2021
  git revision. Resolving it requires one of: a newer macOS-patched
  virglrenderer, a QEMU-side shim for the old renderer API, or dropping the QEMU
  version. A `darwin-arm64` bundle **is** published and pinned in
  `internal/qemubin/version.go` — it is simply built on the plain-HVF branch
  (`GL_ACCEL=0`), so `gpu.mode: virtio` renders in software until an accelerated
  bundle becomes buildable.

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
| **macOS** | `vz` backend (Virtualization.framework) — see below | ✅ Metal-accelerated by the framework; QEMU's `apple-gfx`/`vmapple` path is unusable ([#50](https://github.com/Benehiko/vee/issues/50)) |
| **Windows (arm64)** | 2D only (`ramfb`) + RDP | ❌ No virtio-gpu 3D driver exists for Windows; VFIO unavailable on macOS |

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

### macOS guest

macOS guests do not use QEMU at all — they run on Apple's
Virtualization.framework via vee's `vz` backend, which brings its own
Metal-accelerated display. See "macOS guests (Virtualization.framework)" below.

QEMU's own macOS-guest path (`gpu.mode: apple-gfx` with the `vmapple` machine)
is not usable: upstream compiles that machine out by default because it crashes
on macOS 15.4+ hosts, and it supports macOS 12 guests only. `gpu.mode:
apple-gfx` therefore still errors out, pointing at the vz backend. Tracked in
[issue #50](https://github.com/Benehiko/vee/issues/50).

### Windows guest

`vee create win --template windows` builds Windows 11 arm64 install media on
demand and runs a fully unattended install — see
[docs/windows-guests.md → ARM64 guests](windows-guests.md#arm64-guests) for the
VM shape (NVMe disks, USB install media, ramfb, no TPM device).

There is no production virtio-gpu 3D driver for Windows, and VFIO passthrough (the
only real route to Windows GPU acceleration) is a Linux-host feature. On macOS,
Windows-on-ARM guests get unaccelerated 2D graphics (ramfb); use RDP for a usable
desktop.

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
| Windows | ✅ `win11` / `win10` arm64 media built on demand — 2D display (ramfb), RDP for a desktop; no arm64 Server media exists |

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

## macOS guests (Virtualization.framework)

vee runs macOS guests through Apple's Virtualization.framework — the `vz`
backend — not through QEMU. Backend selection is a per-VM field, so QEMU stays
the default for every other guest:

```sh
vee create mymac --template macos       # restore + provision, then vee ssh mymac
```

Requirements: an Apple Silicon host on macOS 13+, the `vee-vz-helper` binary
(shipped in the `darwin-arm64` release tarball, or `make vz-helper` from a
checkout), 15-20 GB for the cached IPSW plus the guest disk, and one `sudo`
prompt during creation.

Apple's licence permits virtualized macOS **only on Apple hardware, two
instances at a time** — Virtualization.framework enforces the limit itself.

### Architecture

A `VZVirtualMachine` lives inside the process that creates it, so vee spawns
one detached **`vee-vz-helper`** per guest — the analog of a `qemu-system`
process. The helper owns the VM for its lifetime and serves a small
newline-JSON control socket in the VM directory (`status`, `stop`,
`wait-shutdown`), which is what the QMP socket does for a QEMU VM. vee's
lifecycle model is unchanged: PID in `VMState`, liveness by signal, graceful
stop then SIGKILL.

| Concern | QEMU backend | vz backend |
|---|---|---|
| VM process | `qemu-system-aarch64` | `vee-vz-helper` |
| Control channel | QMP unix socket | helper control socket |
| Graceful stop | `system_powerdown` | guest `shutdown -h now` over SSH (macOS ignores `requestStop`) |
| Disk format | qcow2 (and raw) | raw only |
| Firmware/boot | OVMF pflash pair | `VZMacOSBootLoader` + auxiliary storage |
| Guest IP | user-mode hostfwd on 127.0.0.1 | resolved by MAC from host DHCP leases |
| Display | SPICE / cocoa window | guest Screen Sharing (VNC) |
| Guest agent | QGA (optional) | none |

### Creating a guest

1. **Restore image.** `--ipsw latest` (default) asks the host's
   Virtualization.framework for the newest image *this host* can restore.
   A URL or local path pins a specific version; `vee pull macos` pre-fetches.
2. **Install.** The helper runs `VZMacOSInstaller` against a fresh sparse raw
   disk, printing progress. CPU and memory are raised to the image's minimums,
   which are then recorded in `vm.yaml` (`macos.min_cpus`,
   `macos.min_memory_bytes`) and enforced on every later start.
3. **Provision offline** (see below).
4. **Run.** `vee start` spawns the helper; the control socket appearing is the
   start-confirmation gate.

`--macosvm-dir` imports an existing [macosvm](https://github.com/s-u/macosvm)
bundle instead: vee copies the disk and auxiliary storage and reads the
hardware-model and machine-identifier blobs from `macosvm.json` (the same
base64 encoding vee's own config uses). Imported guests are never
re-provisioned.

### First-boot provisioning

A freshly restored guest boots into Setup Assistant, which needs a human at a
display — no SSH, no IP, nothing vee can drive. So before the guest's first
boot, vee patches its disk offline:

1. Attach the raw image without mounting (`hdiutil attach -nomount -plist`).
2. Resolve the guest's APFS **Data**-role volume (`diskutil apfs list -plist`,
   matching the container by physical store — never the host's own volume).
3. Mount it `-o noowners`, which is what lets a non-root user write to a
   foreign APFS volume.
4. Write `.AppleSetupDone`, `Library/User Template/.skipbuddy`,
   `/usr/local/sbin/vee-firstboot.sh` and its launch daemon.
5. Unmount, then fix ownership to `root:wheel` in one batched `sudo` call —
   launchd only loads root-owned daemon plists, and a `noowners` mount records
   the writer's identity instead.

On first boot the guest script creates the admin account (`sysadminctl`),
authorizes your SSH keys, enables Remote Login and Screen Sharing, sets the
hostname, and then deletes itself — it carries the account password, so it is
mode 0700 and removed once provisioning succeeds. Its log stays at
`/var/log/vee-firstboot.log` inside the guest.

The whole step is best-effort: if anything fails after the first file is
written — a failed write, an unexpected disk layout, sudo declined — the
payload is removed again, so the guest still boots into Setup Assistant rather
than landing at a login window with no account. `--skip-first-boot` opts out
deliberately.

The guest password defaults to the account name — it is typed by hand at the
guest login window and at Screen Sharing prompts, and these guests live on a
host-only network. `--password` overrides it, which is worth doing for a
long-lived guest since Remote Login is on. It is printed once and saved to
`macos-credentials.txt` (mode 0600) in the VM directory. SSH uses your vee
key.

### Caveats

- **Screen Sharing needs the guest to boot twice, which `vee create` does for
  you,** because vee has to write its privacy grants offline and the database it
  writes into does not exist any earlier. (An imported or `--skip-first-boot`
  guest gets no grants at all — see below.) Enabling the service leaves the
  guest listening on 5900
  but refusing every session: since macOS 12.1 the agent also needs TCC
  permissions, and macOS only offers those through System Settings or MDM —
  unreachable on a headless guest, as ARD's `kickstart` itself reports ("must
  be enabled from System Settings or via MDM"). So vee authorizes
  `com.apple.screensharing.agent` for `kTCCServiceScreenCapture`,
  `kTCCServicePostEvent` and `kTCCServiceAccessibility` directly in
  `Library/Application Support/com.apple.TCC/TCC.db`, which SIP protects while
  the guest runs but not while its disk is idle on the host.

  macOS creates that database on the guest's *first* boot, not during the
  restore, and a restored guest has never booted — so provisioning never finds
  it. It therefore always records `macos.screen_sharing_grant_pending` in the
  VM config, and every `vee start` retries the grant before launching the
  helper, while the guest disk is still free. The first attempt that finds the
  database writes the grants and clears the flag, so a granted guest never
  attaches its disk again; measured on a macOS 26 guest, that
  attach-mount-write-detach cycle costs about 1.7 s of start time.

  So the guest has to boot twice, and `vee create` completes that cycle itself
  rather than leaving it to be discovered: it waits for the guest's provisioning
  script to write `/var/db/.vee-firstboot-done` (probed over SSH, up to six
  minutes — the wait exists because stopping a guest mid-provisioning would
  leave it without the account and key it is being given), then stops the guest,
  starts it — which is where the grants are written — and waits for it to be
  ready again before claiming anything. `vee view` works when create returns.
  `--no-start` skips the cycle, so those guests get their grants at the second
  `vee start`.

  The step is best-effort: the guest is already installed, so nothing here fails
  a create. Each outcome is reported in its own words, because they send the
  user to different places — provisioning that did not finish in time (with what
  the last probe actually saw), a restart that failed and left the guest
  stopped, a privacy database vee does not recognize (recorded as
  `macos.screen_sharing_unsupported` and never retried), or a grant simply
  deferred to the next start.

  Scope is deliberately narrow: three rows for one client, removed again if
  provisioning rolls back, and nothing written at all when Screen Sharing was
  not requested. Two failures are not retried — a privacy database whose schema
  vee does not recognize is reported once and given up on, since it will not
  become recognizable, and a guest disk vee could not release again fails the
  start outright rather than handing Virtualization.framework an image the host
  still holds. `vee view` still probes port 5900 and reports when nothing
  answers.
- **Terminal type.** A guest has only Apple's terminfo database, so a modern
  emulator's TERM (Ghostty sends `xterm-ghostty`) has no entry and zsh's line
  editor garbles input. `vee ssh` substitutes `xterm-256color` for vz guests
  and says so; QEMU guests are untouched.
- **A per-user setup wizard may still appear at the first graphical login.**
  Suppressing it needs version-stamped `com.apple.SetupAssistant` preferences
  vee does not write yet (lima hits the same wall). SSH is unaffected.
- **No secure token** on the provisioned account, because a launch daemon
  created it rather than Setup Assistant: no FileVault, no startup-security
  changes, no in-place macOS upgrades. Re-restore from a newer IPSW instead.
- **No nested virtualization.** Apple exposes it only to Linux guests
  (`VZGenericPlatformConfiguration`); `VZMacPlatformConfiguration` has no such
  property, and per Apple neither Virtualization.framework nor
  Hypervisor.framework functions inside a VM. Docker Desktop, Podman Machine,
  Lima and UTM therefore cannot run in a macOS guest regardless of host chip —
  even though the M3/M4 host itself supports nesting for Linux guests.
- **Guest ≤ host.** Newer guests will not restore. Newer Apple Silicon also
  sets a floor (an M4 host refuses guests older than macOS 13.4).
- Not testable in CI: GitHub's macOS runners have no nested virtualization, so
  this path is exercised manually on real hardware.

### Why not QEMU's vmapple?

QEMU has an experimental macOS-guest machine (`vmapple` plus `apple-gfx`), and
vee's QEMU backend still carries the seam for it. It is unused: upstream
compiles the machine out by default (`CONFIG_VMAPPLE=n`) because it crashes on
macOS 15.4+ hosts, the fix has sat unmerged since March 2026, and it supports
macOS 12 guests only. Tracked in issue #50; the backend field makes adding it
later a matter of implementing one interface.

## Limitations summary

- No VFIO GPU passthrough (Linux-host kernel feature).
- No virtiofs shares, vhost-vsock SSH-share, swtpm TPM, or bridge networking.
- x86 guests run under slow TCG emulation; use aarch64 guests.
- Accelerated `gpu.mode: virtio` needs a virgl-capable QEMU; stock QEMU = software GL.
- The **virgl-accelerated** vee-qemu bundle is still not buildable (QEMU 10.x vs
  the 2021-era macOS virglrenderer); see "Known limitations of the virgl bundle".
- Venus/Vulkan is experimental. QEMU's `apple-gfx`/`vmapple` macOS-guest path is
  unusable upstream — run macOS guests on the `vz` backend instead.
- macOS guests: no snapshots, suspend/resume, virtiofs shares, `vee check` or
  `vee ports` yet; no SPICE/QMP-derived features (`vee monitor`, `vee qmp`,
  dashboard stats).
