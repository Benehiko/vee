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

## Windows 11 24H2 limitation

`vee pull windows win11` builds **Windows 11 24H2**. Its media **boots into
Windows Setup** (the boot.wim handling above works) and Setup partitions the
disk and begins applying the image — but the **unattended install does not yet
complete on 24H2**.

Windows 11 24H2 replaced the long-standing `setup.exe` with a new "ConX" setup
(`sources\setuphost.exe` / `setupprep.exe`). In an offline, unattended VM the
install fails at ~318 MB with (from `X:\$Windows.~BT\Sources\Panther\setuperr.log`):

```
Error  CDiagnosticsHelper::PersistDiagnosticsData: ...\\?\D:\$WINDOWS.~BT\Sources\Diagnostics: Access is denied [0x00000005]
Error  SetupHost::InitializeOnSettings ... Result = 0x800702E7
Error  SetupHost: OneSettings initialization failed 0x800702E7
```

Root cause: 24H2 Setup places its `$WINDOWS.~BT` working/diagnostics directory
on the **source DVD (D:, read-only)**, hits *Access is denied*, and that
cascades into a fatal failure to initialise Microsoft's **OneSettings** online
service. (Older Setup put `$WINDOWS.~BT` on the writable target disk.)

Bypasses that do **not** work:

- `setup.exe /product server` skips the online/hardware gating, but Setup then
  runs the **Server** codepath and rejects the client `install.wim`
  ("Windows Server installation has failed").
- `setup.exe /Compat IgnoreWarning` has no effect on `0x800702E7`.
- `oobe\bypassnro` / OOBE `BypassNRO` address the later network-account screen,
  not this earlier OneSettings check.

The likely fix (not yet implemented) is to run Setup from a **writable** source
so `$WINDOWS.~BT` lands on writable media — e.g. copy the DVD `sources` +
`setup.exe` to a scratch partition in `startnet.cmd` and launch from there.

**Use `win10` (22H2) for a working end-to-end Windows guest today** — its classic
`setup.exe` flow has no OneSettings gate. Tracking issue: **#17**.

> **Note on 23H2:** pinning `win11` to 23H2 does not help — 23H2's "Windows Setup
> Media" image ships a **stale (2022) `bootmgr`/`cdboot`** while its boot.wim is
> current, so `bootmgr` page-faults in UEFI firmware before WinPE even loads.
> Refreshing the boot files from boot.wim clears that fault but then hits a UEFI
> "No mapping" in the cdboot→bootmgfw chain. 24H2's boot files are self-consistent,
> so 24H2 is the cleaner-booting base; only its OneSettings install stage remains.

## Unattended install details

The `windows` template runs a fully unattended install:

- `Autounattend.xml` on a seed ISO (with the virtio-win drivers) provides locale,
  disk layout (EFI + MSR + Windows), image index, product key, and an autologon
  admin account (`vee` / `vee`).
- The WinPE pass injects the **viostor** (virtio-blk/scsi) and **NetKVM**
  drivers so Setup can see the virtio system disk and network.
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
