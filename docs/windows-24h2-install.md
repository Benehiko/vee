# Installing Windows 11 24H2 as a vee guest

This document is the full account of what it takes to drive an **unattended**
Windows 11 **24H2** install inside a vee VM: every failure encountered, the root
cause of each, the fix, and the end result. It complements
[windows-guests.md](windows-guests.md), which covers the Windows guest pipeline
in general.

If you only want the summary: **`vee create <name> --template windows
--distro-version win11` installs Windows 11 24H2 end-to-end**, booting from the
virtio system disk to the desktop. Getting there required working around four
distinct 24H2-specific failures, described below.

## Background: why 24H2 is different

Every earlier Windows release (including Windows 10 22H2, which vee already
installed cleanly) uses the classic `setup.exe` installer. Windows 11 24H2
replaced it with a new **"ConX" setup** (`sources\setuphost.exe` /
`setupprep.exe`). The behavioural change that matters here:

> ConX Setup writes its `$WINDOWS.~BT` working and diagnostics tree onto **the
> drive `setup.exe` is launched from**, and treats a failure to write there as
> fatal.

vee's media boots WinPE and launches `setup.exe` **directly from the install
DVD**, which is read-only. That single change is the root of the first — and
hardest — failure, and the reason all of the fixes below cluster around "run
Setup from writable media."

The reproduction environment throughout was the stock `windows` template
(`q35,smm=on`, Secure Boot OVMF, TPM 2.0 via swtpm, virtio system disk, two IDE
cdroms) with `--memory 4G`.

## The four failures

### 1. `0x800702E7` — OneSettings initialization failed

**Symptom.** Setup boots, partitions the disk, begins applying the image, then
dies at ~318 MB. `X:\$Windows.~BT\Sources\Panther\setuperr.log`:

```
Error  CDiagnosticsHelper::PersistDiagnosticsData: ...D:\$WINDOWS.~BT\Sources\Diagnostics: Access is denied [0x00000005]
Error  SetupHost: OneSettings initialization failed 0x800702E7
```

**Root cause.** ConX Setup tried to create `$WINDOWS.~BT\Sources\Diagnostics` on
`D:` — the read-only install DVD — got *Access is denied* (`0x00000005`), and
cascaded that into a fatal OneSettings init failure (`0x800702E7`).

**Bypasses that did not work.** `setup.exe /product server` (switches Setup to
the Server codepath, which then rejects the client `install.wim`); `/Compat
IgnoreWarning` (no effect); `BypassNRO` / OOBE account-screen keys (address a
much later screen).

**Fix — run Setup from a writable scratch disk.** The `windows` template now
attaches an extra **8 GB virtio "scratch" disk** alongside the OS disk. In WinPE,
`startnet.cmd`:

1. `drvload`s the viostor driver so the virtio disks are visible (WinRE-based
   WinPE ships no virtio-blk driver);
2. selects the scratch disk, formats it NTFS, and assigns it `W:`;
3. `xcopy`s the entire install DVD onto `W:`;
4. launches `W:\setup.exe`.

Now `$WINDOWS.~BT` lands on the writable `W:` volume, the *Access is denied*
disappears, and OneSettings init succeeds (it degrades to a harmless
`0x80072EE7` "network unreachable" warning in the offline VM).

The scratch disk is the **last** virtio disk on the machine, so it enumerates as
disk 1 while the OS disk stays disk 0 — the answer file's `WillWipeDisk` still
targets disk 0 for the OS install, untouched.

Code: `internal/templates/windows.go` (scratch disk),
`internal/images/windows.go` (`startnet.cmd`).

### 2. WinRE WinPE has no `findstr` or `robocopy`

**Symptom (during fix #1's development).** The scratch disk never got populated;
the console showed `'findstr' is not recognized` and `robocopy ... ERROR 3 ...
W:\ The system cannot find the path specified`.

**Root cause.** Current UUP sets ship no dedicated Setup WinPE, so vee builds
`boot.wim` from the **Recovery Environment (WinRE)** image (see
[windows-guests.md](windows-guests.md)). WinRE's WinPE has a **reduced toolset**:
`findstr.exe` and `robocopy.exe` are **absent**. The first draft of
`startnet.cmd` used both.

**Fix.** Use only tools present in WinRE WinPE: **`find`** (instead of `findstr`)
to filter `diskpart list disk` output, and **`xcopy`** (instead of `robocopy`) to
copy the DVD. A second subtlety: `find "Disk "` also matches the `Disk ###`
header row, so the script drops it with a `find /v "#"` and keeps the last
(highest-index) disk as the scratch.

### 3. `0x80070103` — unattend driver install fails

**Symptom.** With the scratch copy working, Setup reached the real GUI and then
failed: **"Windows installation encountered an unexpected error. Error code:
0x80070103 - 0x40031."** The harvested `setupact.log` showed:

```
IsDriverPackageSigned: File [E:\viostor\w11\amd64\viostor.inf] is signed by a catalog ...
Driver: Received driver inf path [E:\viostor\w11\amd64\viostor.inf].
Error  CDlpActionDriverInstallation::ExecuteUnattendDriverInstall(1393): Result = 0x80070103
```

**Root cause.** Setup found the **correct, signed** viostor driver, but the
**unattend driver-install action itself** failed with `0x80070103`
(`ERROR_NO_MORE_ITEMS`). This is a Microsoft-acknowledged 24H2 regression: ConX
Setup breaks the old `windowsPE` `Microsoft-Windows-PnpCustomizationsWinPE`
`<DriverPaths>` unattend mechanism.

**Fix — inject drivers via `offlineServicing` instead.** The
`PnpCustomizationsWinPE` `<DriverPaths>` block was removed from the answer file.
Drivers now come from two places:

- **WinPE** gets viostor from `startnet.cmd`'s `drvload` (so Setup can see the
  virtio system disk during apply);
- the **installed OS** gets viostor + NetKVM from a new **`offlineServicing`**
  `Microsoft-Windows-PnpCustomizationsNonWinPE` `<DriverPaths>` pass, which DISM
  applies to the offline image after it is deployed — the 24H2-compatible route.

Code: `internal/templates/windows_unattend.go`.

### 4. `0x80070003` — `install.wim` is missing `winre.wim`

**Symptom.** Past the driver step, the OS image began deploying (disk grew into
the multi-GB range), then failed. `setuperr.log`:

```
CExtractFilesFromWIM::DoExecute: Cannot extract file \Windows\System32\Recovery\winre.wim
  from W:\Sources\install.wim. Error 0x80070003
Operation failed: Extract files from WIM ... to G:\$WINDOWS.~BT\Sources\SafeOS. Error: 0x80070003
```

**Root cause.** UUP metadata ESDs keep the Recovery Environment as a **separate
image**. A plain `wimlib` export of the OS image leaves
`\Windows\System32\Recovery` containing only `ReAgent.xml`, **no `winre.wim`**.
Real Microsoft media bundles `winre.wim` inside `install.wim`, and 24H2 Setup's
image-deploy / SafeOS step hard-requires it — extracting it fails with
`0x80070003` (`ERROR_PATH_NOT_FOUND`).

**Fix — inject `winre.wim` into `install.wim` at build time.** The ISO build now
exports the Recovery Environment image from the ESD to a standalone `winre.wim`
and adds it into `install.wim` at `\Windows\System32\Recovery\winre.wim`. The
build logs `WinRE injected into install.wim OK`.

Code: `internal/images/windows.go`.

## End result

With all four fixes in place, a single command:

```
vee create wintest --template windows --distro-version win11
```

drives the install through, unattended, all the way to the desktop:

- WinPE boots → `startnet.cmd` drvloads viostor, formats the scratch disk,
  copies the DVD onto it, and launches Setup from `W:`;
- OneSettings init succeeds (writable `$WINDOWS.~BT`);
- the driver step passes (no WinPE `DriverPaths`);
- the ~7.5 GB image deploys (winre.wim present);
- Setup reboots into the OS **from the virtio system disk** (offlineServicing
  injected viostor), reaches OOBE, and lands on the Windows 11 desktop as the
  `vee` account.

Windows 10 22H2 (classic Setup, no ConX/OneSettings gate) is unaffected by all of
the above and continues to install cleanly.

## Debugging aid: `W:\vee-logs`

Because Setup's Panther logs live on the volatile WinPE RAM disk (`X:`) and are
lost on reboot, `startnet.cmd` harvests them to `W:\vee-logs` on the scratch disk
after Setup exits (`startnet.log` plus each drive's
`$WINDOWS.~BT\Sources\Panther` tree). If a future 24H2 build regresses, stop the
VM and read those logs offline from the scratch qcow2 (e.g. `qemu-img convert -O
raw`, then inspect the NTFS partition) — the setuperr.log is UTF-16.
