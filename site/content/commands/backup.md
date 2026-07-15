---
title: vee backup
weight: 125
---

Back up selected guest directories to the host via rsync over SSH.

```
vee backup <name>
```

A TUI directory picker lets you choose which guest directories to back up. Each run is written to `~/.vee/vms/<name>/backups/<date>/` and recorded in the vee database, so an incomplete previous run can be retried with the same selection.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--list` | `false` | List past backup runs for this VM instead of starting a new one |

## Examples

```sh
# Pick directories and back them up
vee backup myvm

# Review previous backup runs
vee backup myvm --list
```
