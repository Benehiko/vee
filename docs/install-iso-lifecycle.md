# Installer ISO lifecycle

Templates that install an OS from an installer image (TrueNAS, Ubuntu, Windows,
gaming) attach that image as a one-shot `media: cdrom` disk. vee removes the disk
from the VM's saved config once installation completes, so the guest boots from
its primary disk on every subsequent start and never re-enters the installer.

## Install state machine

Each VM carries an `install_state` in its persisted `VMState`:

| State | Meaning |
| --- | --- |
| `""` (empty) | Never started, or created before install tracking existed. |
| `pending` | First boot detected an installer disk; the guest is expected to install itself, then power off (or signal readiness). |
| `ready` | Installation completed. Installer disks are stripped from the saved config. |

On each start (`Manager.Start`):

1. **`SkipInstall`** set + state empty → strip installer disks immediately and
   mark the VM `ready`. Used when attaching a disk that already holds an OS.
2. **State empty** + an installer disk present → mark `pending`.
3. **State `ready`** → strip every installer disk from the config and persist the
   stripped config, so future starts never see the installer at all.

## What counts as an installer disk

`DiskConfig.IsInstallISO()` decides. It returns true when either:

- the disk is explicitly flagged `install_iso: true`, **or**
- the disk has `media: cdrom`.

The `media: cdrom` fallback exists so **legacy configs keep working**. VMs created
before the `install_iso` field existed have installer disks with `media: cdrom`
but no `install_iso` flag. Keying the strip logic on the flag alone would leave a
dead cdrom attached forever — and once the backing ISO is deleted (vee cleans up
downloaded ISOs), every subsequent boot would hard-fail at QEMU's `-drive` open:

```
Could not open '/…/installer.iso': No such file or directory
```

Treating any `media: cdrom` disk as a one-shot installer removes it on the first
start after install completes, regardless of when the config was written.

## Missing-ISO safety net

As a last line of defence, `buildMachine` skips any `media: cdrom` disk whose
backing file no longer exists, logging a warning instead of aborting the boot.
This covers edge cases the state machine cannot — a manually-restored `vm.yaml`,
or an install still marked `pending` whose ISO was removed out of band — so an
already-installed guest can always boot.
