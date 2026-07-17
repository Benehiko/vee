---
title: Configuration
weight: 25
---

Vee reads a single global configuration file at `~/.vee/config.yaml`. It is
created empty on first run, and every key is optional — anything you omit falls
back to a built-in default. Edit the file and the new values take effect on the
next `vee` command.

## Relocating the data directories

The two directories that grow large are the VM storage directory (each VM's
`qcow2` disk images and per-VM state) and the image cache (downloaded ISOs and
cloud base images). Both can live on a different disk or directory of your
choosing:

```yaml
# ~/.vee/config.yaml
storage_path: /mnt/bigdisk/vee/vms     # qcow2 disks + per-VM state
iso_cache_path: /mnt/bigdisk/vee/iso   # downloaded ISOs / cloud images
```

- **`storage_path`** — parent directory for every VM. Each VM gets its own
  subdirectory `<storage_path>/<name>/`. Default: `~/.vee/vms`.
- **`iso_cache_path`** — where `vee pull` caches installer ISOs and cloud base
  images. Default: `~/.vee/iso`.

Vee creates these directories on demand, so a not-yet-existing path is fine as
long as your user can write to the target filesystem and it has enough space.

### Absolute vs. relative paths

An **absolute** path (starting with `/`) is used as written and can point
anywhere. A **relative** path is resolved against `~/.vee` — never against the
directory you ran `vee` from — so `storage_path: vms` means `~/.vee/vms`.

### Moving existing data

Changing a path does **not** move data already on disk. To relocate:

1. Stop running VMs (`vee stop <name>`).
2. Move the directory, e.g. `mv ~/.vee/vms /mnt/bigdisk/vee/vms`.
3. Set the matching key to the new absolute path in `~/.vee/config.yaml`.
4. Start a VM to confirm the new location is used.

The image cache can instead simply be re-populated with `vee pull`.

## Other keys

The same file also configures firmware/binary paths (`ovmf_code_path`,
`qemu_binary_path`, …), VM defaults (`default_memory`, `default_cpus`,
`default_disk_size`, …), and the pacman mirror. See
[the full reference in the repo](https://github.com/Benehiko/vee/blob/main/docs/configuration.md).
