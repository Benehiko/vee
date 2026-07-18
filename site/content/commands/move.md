---
title: vee move
weight: 105
---

Move a VM's managed boot disk to another directory and update its config.

```
vee move <name> <target-dir>
```

Relocates the VM's managed boot `qcow2` disk image into `target-dir` and rewrites
the VM configuration to point at the new location. This is useful for moving a
single VM onto a faster device (e.g. an NVMe drive) without changing the global
`storage_path` for every VM.

Only the boot disk image moves. The rest of the VM's files — `vm.yaml`, logs,
control sockets, UEFI vars and the cidata seed ISO — stay under
`<storage_path>/<name>`. This matches how `vee create --boot-disk-path` places a
new VM's disk.

The VM must be shut down while its disk is moved. If it is running, `vee` stops
it first (prompting for confirmation), performs the move, then starts it again.

The target directory is created on demand. The move uses a rename when the source
and target are on the same filesystem, and falls back to a copy-and-delete across
filesystems.

## Flags

| Flag | Description |
| --- | --- |
| `-y`, `--yes` | Skip all confirmation prompts. Stops the VM if running, moves the disk, and starts it again automatically. Use this for scripting. |
| `--no-start` | Do not start the VM again after the move, even if it was running before. |

## Examples

```bash
# Interactive: prompt before stopping and before restarting.
vee move linux-gaming /mnt/nvme/vms

# Non-interactive: stop, move, and restart without prompts.
vee move linux-gaming /mnt/nvme/vms --yes

# Move the disk but leave the VM stopped afterwards.
vee move linux-gaming /mnt/nvme/vms --no-start
```

## Notes

- VMs booting from a raw host block device (`vee create --boot-disk /dev/...`)
  have no managed disk image to move and are rejected.
- If a file already exists at the target path, the move is aborted so an existing
  disk is never overwritten.
