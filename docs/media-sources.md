# Media sources

The `internal/media` package describes external storage that can be attached to a vee VM. It is the abstraction the `jellyfin` template uses to integrate NFS shares, SMB shares, host directories, raw block devices, and USB devices into a single repeatable `--media` flag. Any future template can use the same machinery.

## Why

Earlier media-server setups on the same machine layered a `bindfs` FUSE remount over a host-side NFS mount to remap ownership for a systemd service running on the host. When the NFS export auto-unmounted, the FUSE layer would not re-attach to the fresh underlying mount, and the consumer service silently lost access until the FUSE mount was torn down and rebuilt.

Pushing the mount into the guest with a systemd `.automount` unit makes the failure mode benign: the unit re-establishes the mount lazily on first access after a network flap, and permissions are governed end-to-end by NFS export configuration plus the guest's local users. No FUSE remap is needed and the host stays unaware of which VM consumes which export.

## Design

`media.Source` is a discriminated union over five kinds: `host-dir`, `nfs`, `smb`, `block`, `usb`. Each source carries an absolute `GuestPath` (where it lands inside the VM) and a kind-specific sub-struct.

`Source.Plan(distro, secrets)` is a pure function that returns a `media.Patch` — a bag of `VirtiofsMount`, `DiskConfig`, `ExtraDevices`, `Packages`, `RunCmds`, and `WriteFiles` fragments that the caller merges into the target `VMConfig` and `CloudInitConfig`. Multiple `Source`s combine cleanly: their patches are concatenated in order.

`Plan` does no I/O and never mutates the caller's config. Secrets (currently just SMB passwords) are surfaced to the caller as `PendingPrompt` values; the caller collects them from the terminal and re-invokes `Plan` with a `secrets` map keyed by `PendingPrompt.Key`. Secrets are never persisted in `vm.yaml`; they are written into the cloud-init cidata ISO on first boot only.

## Per-kind behaviour

| Kind | Patch contents |
|------|----------------|
| `host-dir` | One `VirtiofsMount`; `mkdir -p` + `mount -t virtiofs` runcmds; fstab entry. |
| `nfs` | `nfs-common`/`nfs-utils` package; `.mount` + `.automount` systemd unit files written to `/etc/systemd/system/`; `systemctl enable --now <unit>.automount`. Default `vers=4.2`, options `_netdev,nofail,soft,timeo=30,retrans=2`. |
| `smb` | `cifs-utils` package; credentials file at `/etc/cifs-credentials-<guest-path>` mode `0600`; `.mount` + `.automount` units; enable runcmd. |
| `block` | A `DiskConfig` with `Passthrough: true` and a serial derived from the host disk-by-id path. If `FSType` is set, an fstab entry under `/dev/disk/by-id/virtio-<serial>` and a `mount` runcmd. |
| `usb` | A `usb-host,vendorid=...,productid=...` entry appended to `VMConfig.ExtraDevices` (or `hostbus`/`hostaddr` form). If `MountFSType` is set, the first USB block device is mounted at `GuestPath`. |

## CLI

`vee create ... --media <kind>:<source>@<guest-path>[:<suffix>]` (repeatable). See [`templates/jellyfin.md`](../site/content/templates/jellyfin.md) for the full syntax table and worked examples.

## Adding a new kind

1. Add a `Kind` constant and a sub-struct to `internal/media/source.go`.
2. Implement a `plan<Kind>` function returning a `Patch` and (if needed) `PendingPrompt`s.
3. Add a case to `Source.Plan` to dispatch to it.
4. Cover the kind in `internal/media/source_test.go` with at least one happy-path and one error case.
5. Extend `parseMediaSpec` in `cmd/media.go` if the kind should be reachable from the CLI.
