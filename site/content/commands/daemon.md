---
title: vee daemon
weight: 140
---

Run vee as a long-lived daemon. On startup it starts every VM with `autostart=true`, then polls periodically and restarts any that have exited.

```
vee daemon
```

The daemon is normally launched by the installed system service (systemd on Linux, launchd on macOS) rather than run by hand. It also owns each supervised VM's single QMP connection — which is why [`vee qmp`]({{< relref "qmp" >}}) and [`vee stop`]({{< relref "stop" >}}) route through it when it is running.

## Subcommands

### vee daemon install

On **Linux**, install and enable the vee systemd **system** service. Writes `/etc/systemd/system/vee.service` plus the vfio modprobe, polkit, and udev rules vee needs.

On **macOS**, install and start a system-level launchd **LaunchDaemon** (`/Library/LaunchDaemons/io.vee.daemon.plist`) running as your user account. The Linux-only vfio/polkit/udev steps are skipped.

Requires root/sudo on both platforms.

```sh
sudo vee daemon install
```

### vee daemon uninstall

Disable and remove the service — the systemd unit and its associated polkit/udev files on Linux, the launchd job and its plist on macOS. On macOS, booting the job out sends the daemon SIGTERM, so it gracefully stops any running VMs before exiting.

```sh
sudo vee daemon uninstall
```

## SSH loopback proxies

For every running **bridge-mode** VM with an `ssh_port` configured, the daemon hosts a small TCP proxy on `127.0.0.1:<port>` that forwards to port 22 on the guest's LAN address. Bridge networking has no NAT layer, so QEMU's usual `hostfwd` port-forward does not exist there — the proxy restores the same loopback convenience, giving `vee ssh` and third-party tools (IDE remote-SSH targets, scripts) a stable local endpoint that survives DHCP handing the guest a new address.

The guest's address is resolved per connection (neighbour table by MAC, guest agent as fallback), so the proxy can be up before the guest has finished booting and keeps working across lease renewals. The bound port is recorded in the VM's state and cleared when the VM stops or the daemon exits; [`vee ssh`]({{< relref "ssh" >}}) probes the port before using it, so a VM remains reachable via its LAN address even when the daemon is not running.

## Platform support

The service installer supports Linux (systemd) and macOS (launchd). On Windows the daemon binary runs, but there is no service installer — start it manually if you need autostart supervision.

On host shutdown the daemon powers off running VMs cleanly on both platforms: via logind's `PrepareForShutdown` signal and a block inhibitor on Linux, and via launchd's SIGTERM-with-`ExitTimeOut` contract on macOS. See [Host shutdown](https://github.com/Benehiko/vee/blob/main/docs/host-shutdown.md) for the details, including the macOS caveat that unloading the daemon also stops running VMs.
