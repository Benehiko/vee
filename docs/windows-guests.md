# Windows guests (install ISO pipeline)

vee builds Windows install media on demand — no manual ISO download. It resolves
a build via the [UUP dump](https://uupdump.net/) API, downloads the ESD/CAB
component packages directly from Microsoft's servers, and assembles a bootable
UEFI ISO inside a throwaway container (`wimlib` + `xorriso`). `vee create
<name> --template windows` then boots that media and drives an unattended
install (`Autounattend.xml`, virtio driver injection, TPM/Secure Boot bypass).

```sh
vee pull windows win11             # Windows 11 24H2 (see 24H2 note below)
vee pull windows win10             # Windows 10 22H2
vee pull windows server2025        # Windows Server 2025
vee pull windows server2022        # Windows Server 2022
vee create winvm --template windows --distro-version win11
```

> **Status:** `win10` (22H2) installs end-to-end today. `win11` (24H2) media boots
> into Windows Setup and partitions the disk, but the unattended install does not
> yet complete on 24H2 — see [the 24H2 note](#windows-11-24h2-limitation) below.

## How the ISO is assembled

A UUP set is not a ready-to-burn ISO. vee's container build (`internal/images/windows.go`):

1. Downloads every ESD and CAB component package for the chosen build.
2. Captures the CAB packages into interim ESDs and builds a reference set so
   `wimlib` can resolve cross-container file blobs (`--ref`).
3. Applies the **"Windows Setup Media"** image to lay down the bootable ISO tree
   (`bootmgr`, `/boot`, `/efi`, `setup.exe`) — this leaves `/sources` empty.
4. Produces **`sources/boot.wim`** — the WinPE image the DVD boots into (see
   below) — and exports the OS image to **`sources/install.wim`**.
5. Assembles a hybrid BIOS+UEFI bootable ISO with `xorriso`, using the
   `efisys_noprompt.bin` UEFI boot image so there is no "press any key" prompt.

### boot.wim: WinPE vs. WinRE

Windows install media boots into a WinPE image at `sources/boot.wim` that runs
`setup.exe`. UUP metadata ESDs, however, do not always contain a dedicated Setup
WinPE. Current sets expose only:

- `Windows Setup Media` — the ISO tree (not a boot image)
- `Microsoft Windows Recovery Environment` (WinRE)
- the OS editions

WinRE by itself boots into the **"Choose an option → Troubleshoot" recovery
menu**, not Setup, because its `winpeshl.ini` launches the recovery shell
(`recenv.exe`). vee therefore **transforms WinRE into a Setup boot environment**:
it removes that `winpeshl.ini` and installs a `startnet.cmd` that runs `wpeinit`
and then launches `setup.exe` from whichever drive the install media mounted as.
When a set *does* ship a real Setup/PE image, vee uses that directly.

Without a valid `sources/boot.wim`, the firmware loads `bootmgr` and then fails
in Windows Boot Manager with **`0xc000000f`** ("a required device isn't connected
or can't be accessed").

## Windows 11 24H2: installing from a writable scratch disk

`vee pull windows win11` builds **Windows 11 24H2**, and it installs
**end-to-end** through vee's pipeline (WinPE → Setup → image apply → OOBE →
`vee` desktop, booting from the virtio system disk).

Getting there took working around four 24H2-specific failures. Windows 11 24H2
replaced the long-standing `setup.exe` with a new "ConX" setup
(`sources\setuphost.exe` / `setupprep.exe`) that writes its `$WINDOWS.~BT`
working/diagnostics tree onto **the drive `setup.exe` is launched from**. On a
read-only install DVD that write is denied, and 24H2 treats the failure as fatal:

```
Error  CDiagnosticsHelper::PersistDiagnosticsData: ...D:\$WINDOWS.~BT\Sources\Diagnostics: Access is denied [0x00000005]
Error  SetupHost: OneSettings initialization failed 0x800702E7
```

Registry/flag bypasses do **not** fix this (`/product server` switches Setup to
the Server codepath and rejects the client `install.wim`; `/Compat
IgnoreWarning` has no effect; `BypassNRO` addresses a later OOBE screen). The
real fix is to **run Setup from writable media**, which vee now does:

1. **Scratch disk.** The `windows` template attaches an extra 8 GB virtio
   "scratch" disk (in addition to the OS disk). It is the last virtio disk, so it
   enumerates as disk 1 (OS disk = disk 0). See `internal/templates/windows.go`.
2. **`startnet.cmd` copies the DVD to it.** In WinPE, `startnet.cmd` `drvload`s
   the viostor driver (WinRE has no virtio-blk driver, so the virtio disks are
   otherwise invisible), formats the scratch disk NTFS as `W:`, `xcopy`s the
   whole install DVD onto `W:`, and launches `W:\setup.exe`. Now `$WINDOWS.~BT`
   lands on writable `W:` and the OneSettings failure clears. See
   `internal/images/windows.go`. (WinRE's reduced toolset has no `findstr` or
   `robocopy`, so the script uses `find` and `xcopy`.)
3. **Drivers via `offlineServicing`, not WinPE `DriverPaths`.** 24H2 ConX Setup
   fails the old `windowsPE` `PnpCustomizationsWinPE` `<DriverPaths>` unattend
   step with `0x80070103` (`ERROR_NO_MORE_ITEMS`, shown as `0x80070103-0x40031`)
   — a Microsoft-acknowledged regression. vee instead injects viostor + NetKVM
   through an **`offlineServicing`** `PnpCustomizationsNonWinPE` pass (DISM against
   the applied image), so the installed OS boots from the virtio disk.
4. **`winre.wim` injected into `install.wim`.** UUP metadata ESDs keep the
   Recovery Environment as a separate image, so a plain export leaves
   `\Windows\System32\Recovery` without a `winre.wim`. 24H2 Setup's image-deploy
   step requires it and otherwise fails with `0x80070003`
   (`ERROR_PATH_NOT_FOUND`). vee exports the Recovery Environment image and adds
   it into `install.wim` at build time.

Tracking issue: **#17** (resolved). For the full problem-by-problem writeup of
every 24H2 failure and its fix, see
[windows-24h2-install.md](windows-24h2-install.md).

> **Note on 23H2:** pinning `win11` to 23H2 does not help — 23H2's "Windows Setup
> Media" image ships a **stale (2022) `bootmgr`/`cdboot`** while its boot.wim is
> current, so `bootmgr` page-faults in UEFI firmware before WinPE even loads.
> 24H2's boot files are self-consistent, so 24H2 is the cleaner-booting base.

## Unattended install details

The `windows` template runs a fully unattended install:

- `Autounattend.xml` on a seed ISO (with the virtio-win drivers) provides locale,
  disk layout (EFI + MSR + Windows), image index, product key, and an autologon
  admin account (`vee` / `vee`).
- **WinPE** loads viostor via `startnet.cmd` `drvload` so Setup can see the
  virtio system disk; the **`offlineServicing`** pass injects viostor + NetKVM
  into the installed OS so it boots from the virtio disk and has networking.
- Windows 11 hardware checks (TPM, Secure Boot, RAM, CPU) are bypassed via
  `HKLM\SYSTEM\Setup\LabConfig` keys, since the VM presents a real TPM 2.0 but no
  enrolled Secure Boot keys.
- First-logon runs `setup-guest.ps1` (WinFsp + virtio guest tools) and enables
  the OpenSSH server for headless access.

## Requirements

`nerdctl` or `docker` on `PATH` (the ISO is assembled in a container; no host
tooling is installed) and ~15 GB of free scratch space, allocated next to the
ISO cache (`~/.vee/iso/`) so the build works even when `/tmp` is a small
RAM-backed `tmpfs`.
