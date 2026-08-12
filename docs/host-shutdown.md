# Host shutdown integration

When the vee daemon service is installed (`vee daemon install`), running VMs
are powered off cleanly before the host shuts down or reboots, instead of
being hard-killed. The mechanism is platform-specific: systemd-logind on
Linux, launchd's shutdown contract on macOS (see
[macOS (launchd)](#macos-launchd) below).

## Linux (systemd-logind)

The daemon registers a
[systemd-logind](https://www.freedesktop.org/wiki/Software/systemd/logind/)
**block** inhibitor on `shutdown:sleep` whenever at least one VM is running.
This makes the host wait for guests to power off cleanly before
poweroff/reboot/suspend completes.

### How it works

1. On startup the daemon opens a long-lived D-Bus connection to logind and
   subscribes to `PrepareForShutdown(b)`.
2. Each poll tick (5s) the daemon counts running VMs:
   - **0 → ≥1**: acquire a `block` inhibitor named `vee` with reason
     "Gracefully shutting down running VMs".
   - **≥1 → 0**: release the inhibitor.
3. When logind broadcasts `PrepareForShutdown(true)`, the daemon stops every
   running VM in parallel (60 s per-VM timeout), notifies the desktop, and
   releases the inhibitor — which is what unblocks the host.

You can see the active inhibitor at any time with:

```sh
systemd-inhibit --list
```

### Why dynamic, not always-on?

A `block` inhibitor held permanently makes desktop environments refuse to
shut down. Plasma, for example, shows the shutdown dialog, sees the lock,
and aborts after ~30 seconds with no useful explanation. Holding the
inhibitor only while VMs are actually running means a typical
"all VMs already stopped, now shut down" works without friction.

### KDE / Plasma escape hatch

If the daemon is wedged or the inhibitor logic misbehaves, KDE can be told
to bypass shutdown inhibitors entirely. Run the GUI shutdown the normal way
and accept the override prompt, **or** force it from a terminal:

```sh
# Bypass inhibitors and power off (requires polkit auth):
systemctl poweroff -i

# Or, ask logind directly:
loginctl poweroff -i
```

`-i` (`--ignore-inhibitors`) tells logind to proceed regardless of any held
locks. Use this only when you are sure no VM has unsaved state.

### Tuning the inhibitor delay window

The per-VM stop timeout is 60 s; the daemon allows the whole batch up to
~90 s of wall time. logind's own `InhibitDelayMaxSec` (default 5 s) does
**not** apply to `block` inhibitors — it only caps `delay` mode — so no
logind config changes are required.

### Diagnostics

```sh
# Daemon status and recent logs
systemctl status vee.service
journalctl -u vee.service -e

# Live inhibitor state
systemd-inhibit --list

# Verbose vee daemon logs
tail -F ~/.float/state/logs/vee.log
```

If the host refuses to shut down with no VMs running, check
`systemd-inhibit --list` — if `vee` still appears in the output, the daemon
poll has not yet run (max 5 s) or the release failed (check
`journalctl -u vee.service` for `inhibitor release failed`).

## macOS (launchd)

On macOS, `vee daemon install` writes a system-level **LaunchDaemon**
(`/Library/LaunchDaemons/io.vee.daemon.plist`) that runs `vee daemon` as your
user account, starts it at boot (`RunAtLoad`), and restarts it if it crashes
(`KeepAlive` with `SuccessfulExit=false`, the analogue of systemd's
`Restart=on-failure`). A LaunchDaemon rather than a per-user LaunchAgent is
used for the same reason Linux uses a system unit: it survives logout and is
still alive during host shutdown.

macOS has no logind equivalent — a daemon cannot subscribe to a pre-shutdown
notification or hold a shutdown inhibitor. launchd's entire shutdown contract
is: the job receives **SIGTERM**, gets up to `ExitTimeOut` seconds to exit
(vee sets 300, matching the Linux unit's `TimeoutStopSec`), then is
SIGKILLed. vee maps that contract onto the same graceful-stop path the Linux
daemon uses:

- **SIGTERM → host shutdown.** The daemon treats any SIGTERM as the host
  going down: it stops every running VM in parallel (same 60 s per-VM
  timeout), then exits, which is what lets shutdown proceed. There is no way
  to distinguish `launchctl bootout` from a real power-off, so unloading the
  daemon (including `vee daemon uninstall` or a reinstall) also stops running
  VMs gracefully — they would otherwise be hard-killed moments later on a
  real shutdown, and after a reinstall the fresh daemon's autostart pass
  brings them back.
- **SIGINT (Ctrl-C) → plain stop.** A manually run `vee daemon` interrupted
  from the terminal leaves VMs running, matching `systemctl stop vee` on
  Linux.
- **Sleep, not shutdown, is inhibited.** While at least one VM runs, the
  daemon holds a `caffeinate -i` assertion so the host does not idle-sleep
  under running guests — the macOS analogue of the sleep half of the Linux
  `shutdown:sleep` inhibitor. It is released when the last VM stops.

The Linux-only vfio modprobe, polkit, and udev install steps are skipped on
macOS; there is nothing they would map to.

### Diagnostics

```sh
# Job status (PID, last exit code, plist path)
sudo launchctl print system/io.vee.daemon

# Daemon stdout/stderr (panics, early startup errors)
tail -F ~/.vee/logs/daemon.log

# Structured daemon logs
tail -F ~/.vee/logs/vee.log

# The sleep assertion while VMs run (look for caffeinate)
pmset -g assertions

# Restart after upgrading the vee binary in place
sudo launchctl kickstart -k system/io.vee.daemon
```
