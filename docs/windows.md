# Windows host support

vee can run QEMU virtual machines on a Windows host using the **Windows
Hypervisor Platform (WHPX)** accelerator. This document covers how the Windows
host port works, its prerequisites, and the honest limitations compared with a
Linux host.

> **Scope.** Windows on x86-64 (amd64) is the supported target, running x86-64
> guests under WHPX. VFIO GPU passthrough, virtiofs, vhost-vsock, swtpm, bridge
> networking, CPU pinning, and the systemd daemon installer are Linux-host
> features and are unavailable on Windows — vee degrades gracefully (warns and
> falls back) rather than emitting QEMU arguments that cannot work.

## Host prerequisites

1. **Enable the Windows Hypervisor Platform.** WHPX is an optional Windows
   feature. Enable it (and Hyper-V, which it depends on), then reboot:

   - *Settings → Apps → Optional features → More Windows features* → tick
     **Windows Hypervisor Platform** and **Hyper-V**, or from an elevated
     PowerShell:

     ```powershell
     Enable-WindowsOptionalFeature -Online -FeatureName HypervisorPlatform -All
     Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V -All
     ```

   Virtualization must also be enabled in firmware (BIOS/UEFI). Without WHPX,
   guests fall back to `-accel tcg` software emulation, which is very slow.

2. **Install a WHPX-capable QEMU.** vee shells out to `qemu-system-x86_64`, so a
   QEMU for Windows built with `--enable-whpx` must be on `PATH` (the official
   [QEMU for Windows](https://www.qemu.org/download/#windows) installers include
   WHPX). vee does not yet publish its own `windows-amd64` QEMU bundle, so it
   uses whatever `qemu-system-x86_64.exe` it finds on `PATH`.

**No external ISO tooling is required.** Every cloud-init template needs a
NoCloud seed ISO (`cidata.iso`). Windows ships none of `xorriso` /
`genisoimage` / `hdiutil`, so vee builds the seed with its **built-in pure-Go
ISO9660/Joliet writer** (`internal/cloudinit/iso9660.go`). This produces exactly
the two-file (`user-data`, `meta-data`) `cidata`-labelled image cloud-init's
NoCloud datasource reads, with no external dependency.

## How vee adapts to a Windows host

vee derives host-specific defaults from `internal/platform`:

| Concern | Linux | Windows (amd64) |
|---------|-------|-----------------|
| Accelerator | `-accel kvm` | `-accel whpx` |
| Native guest arch | `x86_64` | `x86_64` |
| Machine type | `q35` | `q35` |
| QEMU binary | `qemu-system-x86_64` | `qemu-system-x86_64.exe` (on `PATH`) |
| QMP / guest-agent transport | AF_UNIX socket file | loopback TCP (`127.0.0.1:<port>`) |
| cidata ISO builder | `xorriso` / `genisoimage` | built-in pure-Go writer |
| Graceful VM stop | QMP `system_powerdown`, then `SIGTERM`→`SIGKILL` | QMP `system_powerdown`, then hard terminate |

### Control channel: loopback TCP instead of unix sockets

On Linux and macOS, vee reaches QEMU's QMP monitor and guest-agent channel over
AF_UNIX socket files inside the VM directory. QEMU on Windows cannot serve those
the same way, so on Windows vee binds each channel to an **ephemeral loopback
TCP port** (`127.0.0.1:<port>`) and records the address in the VM's state so
later commands (`vee stop`, `vee status`) reconnect to the same endpoint. The
port is bound only on `127.0.0.1`; note that QMP itself has no authentication, so
any local process could in principle connect to it — the same trust model as the
unix-socket path, scoped to the local machine.

### Process handoff differences

- `vee ssh` cannot replace its own process with `ssh` (Windows has no `execve`),
  so it spawns `ssh` as a child and waits for it. Behaviour is otherwise the
  same.
- Interactive commands (`vee tunnel`, `vee ssh-share`) register only `Ctrl-C`
  (`os.Interrupt`) for shutdown; Windows does not deliver `SIGTERM`.

## Feature availability

| Feature | Windows host |
|---------|--------------|
| Boot / lifecycle (create, start, stop, status) | ✅ |
| WHPX acceleration for x86-64 guests | ✅ (with WHPX enabled) |
| Nested acceleration (Windows host is itself a VM) | ❌ WHPX can't deliver interrupts to the inner guest |
| QMP / guest-agent control | ✅ (loopback TCP) |
| cloud-init NoCloud seed | ✅ (built-in ISO writer) |
| Serial / SPICE console | ✅ |
| VFIO GPU passthrough | ❌ Linux-only kernel feature |
| virtiofs shared folders | ❌ reference virtiofsd is Linux-only |
| vhost-vsock SSH-agent sharing | ❌ AF_VSOCK is Linux-only |
| Bridge networking | ❌ uses user-mode NAT instead |
| CPU pinning | ❌ relies on `taskset` + `/proc` |
| swtpm software TPM | ❌ swtpm is Linux-only |
| systemd/udev/polkit daemon installer | ❌ no Windows service equivalent yet |

Requests for the unavailable features warn and degrade rather than fail the
build or crash — for example, a VM configured with GPU passthrough on a Windows
host logs that VFIO is unsupported and continues without it.

## Building for Windows

vee cross-compiles cleanly from any host with the Go toolchain:

```sh
make build-windows              # produces vee.exe (windows/amd64)
# or:
GOOS=windows GOARCH=amd64 go build -mod=vendor -o vee.exe .
```

The `install` Makefile target is POSIX-shell and unix-only; on Windows run the
produced `vee.exe` directly. CI cross-compiles and vets `windows/amd64` on every
change (the `build-windows` job) so the port cannot silently regress, and the
release workflow publishes a `windows-amd64` asset.

## Nested virtualization: a Windows *guest* cannot accelerate its own guests

WHPX needs real hardware virtualization. If the Windows host is itself a virtual
machine (for example a Windows guest running under KVM/QEMU on a Linux box), vee
can create and start a guest under `-accel whpx`, but that inner guest **cannot
receive interrupts** and dies shortly after boot. The symptom is:

```
whpx: injection failed, MSI (...) ... lost (c0350005)
WHPX: Unexpected VP exit code 4
```

`c0350005` is `WHV_E_UNKNOWN_CAPABILITY`: WHPX is reporting that the APIC /
interrupt-virtualization capability it needs does not exist. It is absent because
the **outer** hypervisor (the L0 — e.g. KVM) does not expose nested APIC
virtualization (APICv / virtual-interrupt-delivery / posted-interrupts) across
the nested boundary to the L1 Windows guest. KVM does not implement nested APIC
virtualization, and there is no switch for it — not in Windows (no setting or
registry edit can present a CPU capability the layer below withheld), not in
WHPX, and not in QEMU (whose interrupt requests are well-formed but rejected by
the platform). Any guest that needs interrupt delivery — effectively any real
guest — is affected; it is not specific to a particular guest OS or workload.

There is no vee-side fix: vee cannot manufacture a hypervisor capability the host
does not have. When vee runs inside a VM, run it on a **bare-metal** Windows host
instead, where WHPX has full hardware access and this limitation does not apply.
(`vee logs <name>` surfaces the QEMU output above, so the case is recognisable
rather than an unexplained early exit.)

## Limitations summary

- Requires the **Windows Hypervisor Platform** feature enabled (plus Hyper-V and
  firmware virtualization); otherwise guests run under slow TCG emulation.
- Requires a WHPX-capable `qemu-system-x86_64.exe` on `PATH`; vee does not yet
  publish its own `windows-amd64` QEMU bundle.
- **No nested virtualization:** a Windows host that is itself a VM cannot
  hardware-accelerate guests under WHPX — the inner guest fails with `c0350005` /
  `Unexpected VP exit code 4` because the outer hypervisor does not expose nested
  APIC virtualization. See the section above.
- No VFIO passthrough, virtiofs, vhost-vsock, bridge networking, CPU pinning, or
  swtpm (all Linux-host features).
- No Windows service / daemon installer yet — `vee daemon install` targets
  systemd and is Linux-only.
