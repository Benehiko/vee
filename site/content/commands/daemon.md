---
title: vee daemon
weight: 140
---

Run vee as a long-lived daemon. On startup it starts every VM with `autostart=true`, then polls every 30 seconds and restarts any that have exited.

```
vee daemon
```

The daemon is normally launched by the installed systemd service rather than run by hand. It also owns each supervised VM's single QMP connection — which is why [`vee qmp`]({{< relref "qmp" >}}) and [`vee stop`]({{< relref "stop" >}}) route through it when it is running.

## Subcommands

### vee daemon install

Install and enable the vee systemd **system** service. Writes `/etc/systemd/system/vee.service` plus the vfio modprobe, polkit, and udev rules vee needs. Requires root/sudo.

```sh
sudo vee daemon install
```

### vee daemon uninstall

Disable and remove the systemd service and its associated polkit/udev files.

```sh
sudo vee daemon uninstall
```

## Platform support

The systemd installer is Linux-only. On macOS and Windows the daemon binary runs, but there is no service installer — start it manually if you need autostart supervision.

See [Host shutdown](https://github.com/Benehiko/vee/blob/main/docs/host-shutdown.md) for how the daemon blocks host poweroff while VMs are running.
