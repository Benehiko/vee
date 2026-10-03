# Windows guests with DirectX: WinBoat Helios (experimental)

`--gpu-mode=helios` gives a `windows` template guest hardware-accelerated
Direct3D 11/12, Vulkan, OpenGL and OpenCL **without passing a GPU through**. It
uses [WinBoat Helios](https://github.com/winboat-org/helios), a Windows WDDM
display driver for QEMU's virtio-gpu. The guest's graphics calls are translated
to Vulkan (D3D11 via DXVK, D3D12 via vkd3d-proton), carried over virtio-gpu by
Mesa's Venus protocol, and run on the host GPU by virglrenderer.

```
game (D3D11/D3D12)  →  helios_umd(12).dll (DXVK / vkd3d-proton)  →  Mesa Venus ICD
   →  helios KMD  →  virtio-gpu  →  virglrenderer (host)  →  host Vulkan driver  →  host GPU
```

The main use case is running game tests in a Windows 10/11 VM on CI-style hosts
that have a GPU but cannot (or should not) hand one to the VM.

> **Read first:** [windows-directx-evaluation.md](windows-directx-evaluation.md).
> D3D11, Vulkan and OpenGL worked in the test run; **D3D12 hung**, so D3D12-only
> games (Elden Ring, for one) do not run on this today.

> **Tested:** on 2026-10-03 against Helios 22.22.289.0 (commit `52c02799`):
> Arch Linux host, kernel 7.2, AMD RX 9070 (RADV), the `qemu-helios` fork at its
> pinned commit, and Arch's stock virglrenderer 1.3.0. A Windows 11 24H2 guest went
> from `vee create` to a provisioned Helios desktop unattended, and the smoke tests
> below passed. See [Test run](#test-run-2026-10-03).

> **Status: experimental, and so is Helios.** Helios is pre-release: its
> maintainers offer no support, publish no releases, and change it daily.
> D3D11 is its mature path. D3D12 currently tops out at **feature level
> 11_0** natively, and some indirect-draw features games use are missing from
> Venus, so some D3D12 titles will fail for reasons that have nothing to do
> with the game. vee's side has unit tests but has not been run end-to-end
> against every Helios revision. For results that reflect real hardware, use
> GPU passthrough instead ([gpu-passthrough-gaming.md](gpu-passthrough-gaming.md)).

## Host requirements

Linux, x86_64, KVM. From Helios' `HOST.md`:

- Kernel 6.13+ (6.11–6.12 on Intel needs a KVM VMX patch).
- `/dev/udmabuf` (`modprobe udmabuf`; `CONFIG_UDMABUF`).
- A Vulkan 1.3 host driver: RADV (AMD), ANV (Intel), NVK or the NVIDIA
  proprietary driver. Check with `vulkaninfo --summary`.
- Helios' **QEMU fork** (`qemu-helios`) — stock QEMU (including vee's managed
  build) cannot show the Helios scanout and has no `max_hostmem` property.
- Helios' **virglrenderer fork** built with Venus, paired with the fork's
  Venus protocol revision.
- `swtpm`, and `nerdctl` or `docker` for the Windows ISO build (as for every
  `windows` template VM).

### Building the host pieces

**vee does not download or build `qemu-helios`; you build it and pass it with
`--qemu-binary`.** This is deliberate. vee's managed QEMU (`qemubin`, released
by `qemu-release.yml`) is stock QEMU with vee's own patches, pinned by checksum.
Helios' fork is pre-release, changes daily, and in testing could not run D3D12
(see [windows-directx-evaluation.md](windows-directx-evaluation.md)), so
publishing and re-pinning a bundle for it is not worth the upkeep yet. A Helios
VM without `qemu_binary` refuses to start rather than falling back to the
managed QEMU, which cannot show the Helios display. Revisit a managed bundle,
or an on-demand build like vee's `virtiofsd`, if Helios' D3D12 matures.

Build from a Helios checkout so the submodule revisions stay paired:

```sh
git clone https://github.com/winboat-org/helios && cd helios
git submodule update --init qemu-helios
# the fork's ui/vulkan-readback.c needs vendored Vulkan headers
git -C qemu-helios submodule update --init --depth 1 third_party/Vulkan-Headers

# QEMU fork, installed to a prefix so its modules and firmware resolve
cd qemu-helios && mkdir -p build-helios && cd build-helios
../configure --prefix=$HOME/.vee/helios/qemu-install --target-list=x86_64-softmmu \
  --enable-kvm --enable-opengl --enable-virglrenderer --enable-vnc --enable-slirp \
  --enable-modules --disable-docs --disable-werror --disable-gtk --disable-sdl
ninja && ninja install
# then: --qemu-binary $HOME/.vee/helios/qemu-install/bin/qemu-system-x86_64
```

QEMU vendors its own meson, so no system meson is needed. Check the result
with `qemu-system-x86_64 -device virtio-gpu-gl-pci,help | grep max_hostmem`;
only the fork has that property.

**virglrenderer.** Helios pins a virglrenderer fork (`winboat-org/virglrenderer`)
paired with its Venus protocol fork. As of October 2026 that repository is not
public: its submodule URL returns 404. Without it, use the distro's
virglrenderer if it is built with Venus (Arch's 1.3.0 is; check that
`strings /usr/lib/libvirglrenderer.so.1 | grep -i venus` finds something, and
that `virgl_render_server` is installed). According to Helios'
`WINDOWS_CI_PACKAGE.md`, the fork is needed only for the native
device-generated-commands path behind state-changing D3D12 ExecuteIndirect. The
guest only uses Venus extensions the host renderer advertises, so with a stock
renderer that D3D12 path is unavailable and the rest works. If you have the
fork, build it with `bash tools/build-native-renderer.sh` and point QEMU at it
with `--qemu-env` as below.

Helios' `TOOLCHAIN.md` is the source of truth for these steps and their
package prerequisites.

When you do use the paired renderer build, QEMU must load it instead of the
system one. Point it there with `--qemu-env` (vee adds these to QEMU's environment, overriding
inherited values):

```sh
R=$PWD/target/linux/virglrenderer-install
--qemu-env LD_LIBRARY_PATH=$R/lib \
--qemu-env RENDER_SERVER_EXEC_PATH=$R/libexec/virgl_render_server
```

If QEMU, run from its build directory, cannot find its display modules or
firmware ROMs, either `meson install` it to a prefix and use that binary, or
add `--qemu-env QEMU_MODULE_DIR=<build-helios>`.

### Getting `HeliosSetup.exe`

Helios has no GitHub releases. The guest installer comes from its
**"Windows graphics and compute bundle"** GitHub Actions workflow
(`helios-windows-x64-<version>-Release` artifact →
`helios-windows-x64-*.zip` → `HeliosSetup.exe`), or from building it on Windows
with `ci/windows/Build-Driver.ps1` and friends. The bundle is test-signed with
a throwaway key; that is why the VM runs with Secure Boot off.

## Create and run

```sh
vee create wintest --template windows --distro-version win11 \
  --gpu-mode helios \
  --helios-setup ~/Downloads/HeliosSetup.exe \
  --qemu-binary ~/src/helios/qemu-helios/build-helios/qemu-system-x86_64 \
  --qemu-env LD_LIBRARY_PATH=$R/lib \
  --qemu-env RENDER_SERVER_EXEC_PATH=$R/libexec/virgl_render_server \
  --gpu-hostmem 12G

vee start wintest
vee helios status wintest --wait     # Windows install + driver provisioning
vee helios verify wintest            # Helios' own D3D/Vulkan/GL/CL smoke tests
vee view wintest                     # the desktop, over VNC
```

| Flag | Meaning |
|------|---------|
| `--gpu-mode helios` | Helios GPU; `windows` template only |
| `--helios-setup PATH` | `HeliosSetup.exe`; installed unattended at first logon (required) |
| `--qemu-binary PATH` | the `qemu-helios` binary (required) |
| `--qemu-env KEY=VALUE` | QEMU process environment, repeatable |
| `--gpu-hostmem SIZE` | host memory window used as the guest's "VRAM" (default 8G). It is host RAM, on top of the guest's own memory |
| `--vnc HOST:DISPLAY` | VNC address; default is loopback port 5910–6009, derived from the VM name |
| `--render-node PATH` | pin to one host GPU, e.g. `/dev/dri/renderD129` |
| `--helios-bootstrap-vga` | standard VGA adapter during the Windows install only (see Troubleshooting) |

The same settings exist on the MCP `vm_create` tool (`gpu_mode`,
`helios_setup`, `qemu_binary`, `qemu_env`, `vnc`, `render_node`,
`helios_bootstrap_vga`), and in `vm.yaml` afterwards (`qemu_binary`,
`qemu_env`, `gpu.host_mem`, `gpu.vnc`, `gpu.render_node`).

### What the VM looks like

Compared to a plain `windows` VM:

- **GPU**: `virtio-gpu-gl-pci,max_outputs=1,blob=true,venus=true,hostmem=X,max_hostmem=X`,
  no legacy VGA — the same device Helios' launcher uses.
- **Display**: `-display egl-headless[,rendernode=…]` plus `-vnc`. The GTK/SDL
  GL windows cannot show Helios' scanout, and there is no host window at all,
  which also suits CI. SPICE is not used.
- **Firmware**: plain OVMF and no secure-pflash: Secure Boot is **off**, because
  Windows refuses test signing while it is on. The TPM stays, and the answer
  file's LabConfig keys still bypass the Windows 11 checks.
- **Size**: 16G RAM, 8 vCPUs (override with `--memory` / `--cpus`).

### What happens in the guest

1. The unattended Windows install runs as for any `windows` VM. VNC shows the
   firmware console, then nothing until first logon: Windows Setup cannot draw
   on the non-VGA virtio-gpu (see Troubleshooting).
2. The first-logon script installs the virtio-win guest tools (which bind the
   `viogpudo` display driver), enables OpenSSH, then copies `HeliosSetup.exe`
   from the extras ISO to `C:\ProgramData\vee\helios\` and runs
   `HeliosSetup.exe --silent --automatic`. That enables test signing and
   registers Helios' own at-startup resume task. Its `-Automatic` mode replaces
   `viogpudo`.
3. The first-logon script also enables persistent auto-logon for `vee`, so every
   boot lands in the desktop session that Helios' smoke tests (and games) need.
4. Helios reports progress in `C:\ProgramData\Helios\provisioning-status.json`
   (`waiting` → `test-signing-restart-required` → `driver-restart-required` →
   `finished`, or `failed`), but leaves the reboots to the caller. vee
   registers a `VeeHeliosWatch` startup task (running as SYSTEM) that reboots once
   per *new* restart-required status. It caps reboots at four and removes itself
   on `finished` or `failed`.

The guest reboots itself two or three times. `vee helios status --wait` follows
along, retrying while the guest is unreachable.

## Running tests

`vee helios verify` runs Helios' `Verify-Helios.ps1 -RunSmokeTests`: it checks
the installed files and registrations, then creates Vulkan instances, D3D11 and
D3D12 devices and WGL contexts in x64 and x86 processes, reads back cleared
textures and runs an OpenCL kernel. The probes refuse to run in session 0, where
SSH sessions live. vee therefore runs them as an interactive scheduled task in
the auto-logged-on `vee` desktop session and relays the log. It exits non-zero
on any failure. A pass does not prove presentation correctness or full
conformance.

Games and test harnesses that create windows or swapchains have the same
constraint: run them in the desktop session, not straight from `vee ssh`. The
simplest way is the same pattern as `verify`, a scheduled task with an
interactive logon for the `vee` user:

```powershell
$a = New-ScheduledTaskAction -Execute 'C:\tests\run.cmd'
$p = New-ScheduledTaskPrincipal -UserId vee -LogonType Interactive -RunLevel Highest
Register-ScheduledTask -TaskName GameTest -Action $a -Principal $p -Force
Start-ScheduledTask -TaskName GameTest
```

Copy builds in with `vee cp` or share them with `--virtiofs-dir`. Use
`vee screenshot` or the VNC display to look at the result.

A CI-style flow:

```sh
vee start wintest
vee helios status wintest --wait --timeout 45m
vee helios verify wintest
vee cp -r ./game-build 'wintest:C:\tests'
vee ssh wintest -- powershell -NoProfile -File C:\tests\launch-interactive.ps1
```

## Test run (2026-10-03)

| Step | Result |
|------|--------|
| ISO build + `vee create` | Windows 11 24H2 (26100.6508), about 6 min |
| Unattended install to desktop | about 13 min (OS disk on an HDD), one QEMU process |
| First-logon script | WinFsp, virtio-win guest tools, OpenSSH, keys, then `HeliosSetup.exe --silent --automatic` |
| Provisioning | `waiting` → `test-signing-restart-required` (handled by the setup script's reboot) → `driver-restart-required` (one reboot by `VeeHeliosWatch`) → `finished`, about 3 min |
| Display adapter | "Helios vGPU", provider WinBoat, OK; test signing on |
| Desktop on VNC | Helios scanout through the fork's Vulkan readback, about 1 ms per frame |
| Vulkan | `Virtio-GPU Venus (AMD Radeon RX 9070 (RADV GFX1201))`, API 1.4.343 |
| D3D11 x64 | device on Helios, feature level 11_0, clear/copy/readback: 527/527 pixels match |
| OpenGL | 4.6 compatibility, Mesa zink on Venus |
| OpenCL, GL/CL sharing, mixed GL/D3D11 | pass |

Observed on this stack:

- Each D3D11 probe sits idle for about 90 s before creating its device. It is slow, not hung.
- `dwm.exe` crashed twice with `0xC00001AD` (fatal memory exhaustion in
  `dwmcore.dll`), and Explorer crashed once, while guest RAM and GPU memory
  were far from full. Windows restarted them and the desktop recovered. This is
  a Helios/renderer stability issue, not something vee controls. Keep it in
  mind when a game test fails on window creation or presentation. The run used
  `--gpu-hostmem 4G`, half Helios' 8G default, and a stock virglrenderer
  instead of Helios' fork.

## Troubleshooting

- **VNC says "Display output is not active" during the Windows install.**
  This is expected. The Helios device is a non-VGA `virtio-gpu-gl-pci`, and
  neither OVMF nor Windows Setup turn on a scanout on it, so nothing is shown
  until the virtio-win display driver loads at first logon. The install is
  unattended and runs regardless; follow it with
  `qemu-img info -U <disk>` (actual size grows to roughly 20 GB) or wait for
  SSH. To watch it, recreate the VM with
  `--helios-bootstrap-vga`. This adds a standard VGA adapter only until the
  install is marked complete, as Helios' own launcher does for install media.
  While it is attached, VNC shows *that* adapter, not Helios. It is removed on
  the first `vee start` after the install, so stop/start the VM once
  provisioning finishes.
- **`vee start` fails with `max_hostmem` / unknown property.** `--qemu-binary`
  is not the Helios fork.
- **QEMU exits immediately mentioning virglrenderer or the render server.**
  The fork is loading the system virglrenderer. Set `LD_LIBRARY_PATH` and
  `RENDER_SERVER_EXEC_PATH` with `--qemu-env` (or `qemu_env` in `vm.yaml`).
  `vee logs wintest` shows QEMU's stderr.
- **Provisioning `failed`.** Read `C:\vee-helios.log` (vee's watch task),
  `C:\ProgramData\vee\helios\setup.log` (the installer), and
  `C:\vee-guest-setup.log` (first-logon script). A `Secure Boot is enabled`
  error means the VM was booted with secboot firmware. Check
  `uefi.code_path` in `vm.yaml` points at the plain `OVMF_CODE` build.
- **Stuck in a restart-required status.** The watch task never reboots twice for
  the same status. Check Helios' `HeliosGraphicsProvisioning` task in Task
  Scheduler, and reboot once manually (`vee ssh wintest -- shutdown /r /t 0`).
- **`verify` times out.** The probes need the desktop session. Make sure the
  guest auto-logged on (look at VNC). Each probe can take one to two minutes,
  so the full suite may run past the default 5 minutes: raise `--timeout`. The
  in-guest log is `C:\ProgramData\vee\helios\verify.log`.
- **The guest sits at the sign-in screen.** Helios VMs keep auto-logon for the
  `vee` account after every boot; the plain answer file only auto-logs on once,
  and provisioning reboots use that up. A VM created before this change needs
  `AutoAdminLogon=1`, `DefaultUserName=vee` and `DefaultPassword=vee` under
  `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon`, and
  `AutoLogonCount` removed.
- **Multi-GPU host renders on the wrong GPU.** Pass `--render-node`.
- **`vee screenshot` fails with `resource temporarily unavailable`.** QMP
  accepts one client, and the vee process watching the VM (the daemon, or a
  `vee start` still waiting for SSH) holds it. Capture through the VNC display
  instead, for example `gvnccapture 127.0.0.1:<display> shot.png` (gtk-vnc).
- **The VNC viewer freezes or closes when something else connects.** QEMU's
  default VNC share policy lets a client that asks for exclusive access
  disconnect every other viewer, and some clients ask by default (`gvnccapture`
  does). vee starts Helios VMs with `share=force-shared`, so viewers and capture
  tools coexist. VMs started by an older vee get it on their next start.
- **Upgrade the daemon before running Helios VMs.** Once an install is ready,
  a guest reboot becomes a full QEMU relaunch by whichever vee process watches
  the VM, which is usually the daemon. A daemon still running an older vee
  binary does not know `qemu_binary` or `gpu.mode: helios`, so it relaunches
  the guest without the Helios GPU, on the wrong QEMU. Restart it after
  installing this vee: `sudo systemctl restart vee`.

## Limits

- x86_64 Linux hosts and x64 Windows guests only.
- One output, and no SPICE. Use VNC; tunnel it over SSH for remote viewing,
  because the VNC server has no password and binds loopback by default.
- Helios' own limits apply: see its `README.md`, `DX12.md` and `ROADMAP.md`.
