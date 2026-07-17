# Global configuration

Vee reads a single global configuration file at `~/.vee/config.yaml`. The file
is created empty on first run, and every key is optional — anything you omit
falls back to a built-in default. Edit the file, then run any `vee` command; the
new values take effect on the next invocation (there is no daemon to restart for
config to apply).

## Relocating the data directories

The two directories that grow large are the VM storage directory (which holds
each VM's `qcow2` disk images and per-VM state) and the image cache (which holds
downloaded ISOs and cloud base images). Both can live on a different disk or
directory of your choosing.

```yaml
# ~/.vee/config.yaml
storage_path: /mnt/bigdisk/vee/vms     # qcow2 disks + per-VM state
iso_cache_path: /mnt/bigdisk/vee/iso   # downloaded ISOs / cloud images
```

- **`storage_path`** — parent directory for every VM. Each VM gets its own
  subdirectory (`<storage_path>/<name>/`) containing its `qcow2` disk(s),
  `vm.yaml`, logs, and cloud-init `cidata.iso`. Default: `~/.vee/vms`.
- **`iso_cache_path`** — where `vee pull` downloads and caches installer ISOs and
  cloud base images so later VMs reuse them instead of re-downloading. Default:
  `~/.vee/iso`.

Vee creates these directories on demand — point them at a path that does not
exist yet and it will be created the first time a VM is built or an image is
pulled. Make sure the target filesystem has enough free space and that your user
can write to it.

### Absolute vs. relative paths

- An **absolute** path (starting with `/`) is used exactly as written, so it can
  point anywhere — another disk, an NFS mount, a dedicated volume.
- A **relative** path is resolved against `~/.vee`, never against the directory
  you happened to run `vee` from. For example `storage_path: vms` resolves to
  `~/.vee/vms`. This keeps VM disks from accidentally landing in an arbitrary
  working directory.

### Moving existing data

Changing `storage_path` or `iso_cache_path` does **not** move data that is
already on disk. To relocate an existing setup:

1. Stop any running VMs (`vee stop <name>`).
2. Move the existing directory to the new location, e.g.
   `mv ~/.vee/vms /mnt/bigdisk/vee/vms`.
3. Set the matching key in `~/.vee/config.yaml` to the new absolute path.
4. Start a VM again to confirm it reads from the new location.

The image cache (`iso_cache_path`) can also simply be re-downloaded with
`vee pull` if you would rather not move it.

## Per-VM boot disk location

`storage_path` moves *every* VM. To place just one VM's boot disk elsewhere — for
example a single Windows VM's disk on a fast NVMe while the rest stay on the
default disk — pass `--boot-disk-path` to `vee create`:

```sh
vee create win --template windows --boot-disk-path /mnt/nvme
```

- The value is a **directory**. Vee keeps its generated disk filename inside it,
  so the boot disk lands at `/mnt/nvme/disk-win-<size>.qcow2`. The directory is
  created on demand.
- Only the **boot disk image** moves. The rest of the VM directory (`vm.yaml`,
  `state.json`, logs, control sockets, UEFI `OVMF_VARS.fd`, the cloud-init
  `cidata.iso`) stays under `<storage_path>/<name>/`. This is the same split that
  already applies to raw-device passthrough VMs.
- The resolved path is written into the VM's `vm.yaml`, so restarts, backups, and
  QMP all use the new location.

This is different from `--boot-disk`, which boots from a **raw host block device**
(`/dev/disk/by-id/...`) via passthrough rather than a managed qcow2 image. If you
pass a raw `--boot-disk` there is no managed qcow2 disk to relocate, so
`--boot-disk-path` has no effect.

## Other configurable paths

These default to locations under `~/.vee` and rarely need changing, but the same
absolute/relative rules apply:

| Key | Purpose | Default |
| --- | --- | --- |
| `log_path` | Directory for the structured `vee.log` | `~/.vee/logs` |
| `qemu_binary_path` | QEMU system binary (bare name resolves via `$PATH`) | vee-managed binary if present, else `$PATH` |
| `virtiofsd_path` | `virtiofsd` binary for shared-folder mounts | `~/.vee/bin/virtiofsd` or `/usr/bin/virtiofsd` |
| `bridge_helper_path` | setuid `qemu-bridge-helper` (Linux bridge networking) | probed per distro |
| `ovmf_code_path` | UEFI firmware code image | probed per distro / vee-managed bundle |
| `ovmf_vars_path` | UEFI firmware writable vars template | probed per distro / vee-managed bundle |
| `ovmf_secboot_code_path` | UEFI Secure Boot firmware code image | probed per distro / vee-managed bundle |

## VM defaults

The global config also sets the defaults applied to new VMs when you do not pass
an explicit flag to `vee create`:

```yaml
default_memory: 8G           # RAM
default_cpus: 2              # vCPUs
default_disk_size: 20G       # primary disk size
default_cpu_model: host      # QEMU -cpu model
default_machine_type: q35    # QEMU machine type (host-arch derived)
recreate_disks: false        # recreate disks on every create
```

## Mirror settings

See [Host-side pacman caching proxy](pacman-mirror.md) for `mirror_mode` and
`mirror_address`.

## Full example

```yaml
# ~/.vee/config.yaml

# Store bulk data on a separate disk.
storage_path: /mnt/bigdisk/vee/vms
iso_cache_path: /mnt/bigdisk/vee/iso

# Larger default VMs.
default_memory: 16G
default_cpus: 8
default_disk_size: 60G
```
