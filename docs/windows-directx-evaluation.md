# DirectX in a Windows guest: the WinBoat Helios evaluation (October 2026)

This is the account of an attempt to give a vee Windows 10/11 guest
hardware-accelerated DirectX **without passing a GPU through**, so games could
be tested inside Windows. Specifically, the goal was Elden Ring with the
`elden-ring-mods-engine` test loop. It records what was built, how it was
tested, what worked, what did not, and what to do instead.

**Outcome:** vee can now create and provision a Windows guest running the
[WinBoat Helios](https://github.com/winboat-org/helios) paravirtual GPU driver.
D3D11, Vulkan, OpenGL and OpenCL worked on the guest. **Direct3D 12 did not**,
and Elden Ring is D3D12-only. Helios is not usable for that goal today. The test
VM, its disks and all build artefacts were removed afterwards. The vee support
code stays, documented as experimental in [windows-helios.md](windows-helios.md).

## What Helios is

Helios is a Windows WDDM display driver for QEMU's virtio-gpu, pre-release and
explicitly unsupported by its authors. Guest graphics APIs are translated to
Vulkan and carried to the host GPU over virtio-gpu's Venus protocol:

```
game (D3D11/D3D12) → helios_umd(12).dll (DXVK / vkd3d-proton) → Mesa Venus ICD
  → Helios KMD → virtio-gpu → virglrenderer (host) → host Vulkan driver → host GPU
```

It needs its own QEMU fork (`qemu-helios`) for the display path, and pairs with
its own virglrenderer and Venus-protocol forks. The guest bundle
(`HeliosSetup.exe`) is test-signed, so Windows must run with test signing on and
Secure Boot off.

## What was built in vee

All of it is behind `--gpu-mode=helios` on the `windows` template, x86_64 Linux
hosts only.

| Area | Change |
|------|--------|
| GPU device | `virtio-gpu-gl-pci,max_outputs=1,blob=true,venus=true,hostmem=X,max_hostmem=X` (the device Helios' launcher uses) |
| Display | `-display egl-headless[,rendernode=…]` plus `-vnc` on a per-VM loopback port, `share=force-shared` |
| QEMU | per-VM `qemu_binary` (the fork) and `qemu_env` (KEY=VALUE overrides, e.g. a paired virglrenderer) |
| Firmware | plain OVMF, no secure pflash, so Secure Boot is off. The TPM is kept |
| Guest provisioning | `HeliosSetup.exe` on the extras ISO, run `--silent --automatic` at first logon; a `VeeHeliosWatch` startup task reboots once per new `*-restart-required` status, capped at four |
| Desktop session | persistent auto-logon for the `vee` account (Helios VMs only) |
| Install display | optional `--helios-bootstrap-vga`: a standard VGA adapter for the install only, stripped afterwards via `install_devices` |
| Commands | `vee helios status [--wait]`, `vee helios verify` (Helios' own smoke tests, run in the desktop session); MCP `vm_helios_status`, `vm_helios_verify`; `vee view` opens the VNC display |

## The test run

Host: Arch Linux, kernel 7.2, i7-13700KF, AMD RX 9070 (RADV) driving the host
desktop, RX 7900 bound to `vfio-pci` for other VMs. Guest: Windows 11 24H2
(26100.6508), 8G RAM, 6 vCPUs, `--gpu-hostmem 4G`, OS disk on a spare HDD.

| Step | Result |
|------|--------|
| Helios guest bundle | 22.22.289.0 (commit `52c02799`) from Helios' CI workflow; Helios publishes no releases |
| `qemu-helios` | built at its pinned commit; needed the vendored Vulkan headers submodule, installed to a prefix |
| virglrenderer fork | **not available**: `winboat-org/virglrenderer` returns 404. Arch's stock virglrenderer 1.3.0 (Venus-enabled) was used instead |
| ISO build + create | about 6 min |
| Unattended install to desktop | about 13 min, in one QEMU process |
| Helios provisioning | `waiting` → `test-signing-restart-required` → `driver-restart-required` → `finished`, one reboot by `VeeHeliosWatch`, about 3 min |
| Display adapter | "Helios vGPU", provider WinBoat, OK |
| Desktop on VNC | worked, through the fork's Vulkan readback |
| Vulkan x64/x86 | **pass**: `Virtio-GPU Venus (AMD Radeon RX 9070 (RADV GFX1201))`, API 1.4.343 |
| D3D11 x64/x86 | **pass**: device on Helios at feature level 11_0, 527/527 readback pixels. Each probe sits idle about 90 s first |
| OpenGL 4.6 (zink), OpenCL, GL/CL sharing, mixed GL/D3D11 | **pass** |
| Vulkan WSI x86 (swapchain present) | stopped after its first phase; likely failed |
| **D3D12 x64** | **hung**: no progress for over 16 minutes with near-zero CPU. The verify run never finished |

## What did not work, and why

1. **D3D12.** The D3D12 smoke probe hung. Helios' own `docs/dx12/FEATURE_LEVELS.md`
   describes native feature level 12_0/12_1 as work in progress and "not
   claimed". The run also lacked Helios' paired virglrenderer fork, which its
   documentation says some D3D12 paths depend on. Either way, D3D12 is not
   usable on this stack today.
2. **Elden Ring is D3D12-only.** Checked against the installed game, not
   assumed: `eldenring.exe` imports `d3d12.dll` and `dxgi.dll` and nothing from
   `d3d11.dll`; its internal name is `GR_win64_MasterLTO_SteamWW_d3d12.exe`; and
   under Proton it builds a `vkd3d-proton.cache`, with no DXVK cache. Whether it
   needs feature level 12_0 specifically was not verified.
3. **Compositor instability.** `dwm.exe` crashed twice with `0xC00001AD`
   (fatal memory exhaustion in `dwmcore.dll`), and Explorer crashed twice. Guest
   RAM (5.7 of 8 GB free) and GPU memory (about 600 MB of 4 GB) were nowhere near
   full. Windows restarted both each time.
4. **Scanout buffer mismatch.** The fork's Vulkan readback repeatedly rejected
   guest scanouts (`OPTIMAL DMA-BUF shape mismatch required=4587520
   fd_size=4096000`) and fell back to an EGL import. The likely cause is the stock
   virglrenderer allocating differently from Helios' fork.
5. **ermod-engine has no Windows launcher.** Independently of Helios, the
   `elden-ring-mods-engine` launcher deliberately refuses a Windows build
   (`build.zig`): staging, injection and the Easy Anti-Cheat gate are unresolved
   design decisions. Its rig and test loop assume Linux machines running the game
   through Proton. So no Windows guest, Helios or otherwise, can run that test
   loop until such a launcher exists.
6. **Anti-cheat versus test signing.** Easy Anti-Cheat does not run with
   Windows test signing on, which Helios requires. Launching `eldenring.exe`
   directly (offline) avoids that, as the mod engine already does under Wine.

## Helios forks

All nine forks of `winboat-org/helios` were compared against upstream `master`
on 2026-10-03. None adds D3D12 capability. Five are plain copies, 299 or more
commits behind. The rest add driver diagnostics (wizardkof `p06/e1-*`, 31
commits), a 120 Hz virtual monitor (alppp), an Unreal Engine 5.7 timing mode
(Fairlightish), driver shutdown fixes (chandshy), and 12 old CI/hardening commits
(TibixDev). D3D12 work happens upstream: `master`, which the tested bundle came
from, and the older `wddm-dx12` branch.

## vee bugs found and fixed along the way

These fixes are not Helios-specific:

- **SSH handshake had no deadline.** QEMU's user-mode port forward accepts TCP
  before the guest's sshd exists, so `vee wait --timeout` (and every SSH probe)
  could block forever. The handshake is now bounded.
- **Opening an SSH session had no deadline.** A guest that finished the
  handshake and then rebooted left `NewSession` waiting forever. It now honours
  the caller's context.
- **`vee delete` orphaned relocated boot disks.** A disk placed with
  `--boot-disk-path` outside the VM directory was left behind. It is now removed,
  but only if it carries vee's generated file name.

Each has a regression test. The two SSH hangs were reproduced against the
unfixed code.

Helios-specific problems found by the live run were fixed in the
template and commands before the run continued:

- The answer file auto-logs on once, and the provisioning reboots used that up,
  leaving the guest at the sign-in screen. Smoke tests (and games) need a
  desktop session, hence persistent auto-logon.
- `vee helios verify` used PowerShell's own output redirection, which in
  Windows PowerShell 5.1 turns a probe's harmless stderr lines into
  terminating errors. It now runs the script as a child process with OS-level
  redirection.
- A capture tool asking for exclusive VNC access disconnected the user's viewer,
  hence `share=force-shared`.
- The install shows nothing on VNC between the firmware and first logon (the
  non-VGA device has no linear framebuffer for Windows Setup). That is expected
  and documented, not fixed.

## Alternatives

For **real D3D12 in a Windows guest** on this host, pass a whole GPU through.
The RX 7900 is already bound to `vfio-pci`. vee supports it
(`--gpu-mode passthrough --gpu-pci 08:00.0`, including the Navi reset
workaround), though not yet tested with the `windows` template, and only one VM
can own the card at a time. The display needs the GPU's own output,
Sunshine/Moonlight, or Looking Glass.

For **Elden Ring mod testing**, stay on Linux guests running the game through
Proton/vkd3d-proton: Venus (like `venus-test`) or a passed-through GPU. That is
what `elden-ring-mods-engine` supports.

Ruled out on this hardware:

- **SR-IOV / mediated GPUs:** consumer Radeon cards do not support it, the
  13700KF has no integrated GPU, and NVIDIA vGPU needs NVIDIA datacenter cards.
- **Hyper-V GPU partitioning:** needs a Windows host.
- **VMware's virtual GPU:** stops at D3D11.

Outside this host: a physical Windows machine, or cloud Windows GPU instances.
Mod tests there still need the Windows launcher from item 5 above.

## If Helios is revisited

- Watch Helios' `FEATURE_LEVELS.md` and `ROADMAP.md` for native FL12_0 and a
  public virglrenderer fork. Re-test with the paired renderer and Helios'
  default `--gpu-hostmem 8G`.
- Restart the vee daemon on the new binary first: once an install is ready, a
  guest reboot becomes a QEMU relaunch by whichever vee process watches the VM,
  and an older daemon relaunches without the Helios GPU.
- Run `vee helios verify --timeout 20m`: probes take one to two minutes each.
- Host setup, flags and troubleshooting are in [windows-helios.md](windows-helios.md).
