---
title: vee mirror
weight: 150
---

Manage the host-side pacman caching proxy ([pacoloco](https://github.com/anatol/pacoloco)) used by Arch VMs. It runs as a systemd `--user` unit on `localhost:9129` so identical packages aren't re-downloaded every time you create an Arch VM.

```
vee mirror <subcommand>
```

## Subcommands

### vee mirror start

Install (if needed) and start the pacoloco user unit, then print its status.

```sh
vee mirror start
```

### vee mirror status

Show the unit state and cache size — unit name, active/inactive, whether it's installed, listener URL, guest URL, cache directory, and cache size.

```sh
vee mirror status
```

### vee mirror stop

Stop and disable the pacoloco user unit.

```sh
vee mirror stop
```

### vee mirror purge

Delete all cached packages on disk.

```sh
vee mirror purge
```

See [docs/pacman-mirror.md](https://github.com/Benehiko/vee/blob/main/docs/pacman-mirror.md) for how the mirror is wired into Arch guests.
