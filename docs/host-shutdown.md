# Host shutdown integration

When `vee.service` is installed (`vee daemon install`), the daemon registers
a [systemd-logind](https://www.freedesktop.org/wiki/Software/systemd/logind/)
**block** inhibitor on `shutdown:sleep` whenever at least one VM is running.
This makes the host wait for guests to power off cleanly before
poweroff/reboot/suspend completes.

## How it works

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

## Why dynamic, not always-on?

A `block` inhibitor held permanently makes desktop environments refuse to
shut down. Plasma, for example, shows the shutdown dialog, sees the lock,
and aborts after ~30 seconds with no useful explanation. Holding the
inhibitor only while VMs are actually running means a typical
"all VMs already stopped, now shut down" works without friction.

## KDE / Plasma escape hatch

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

## Tuning the inhibitor delay window

The per-VM stop timeout is 60 s; the daemon allows the whole batch up to
~90 s of wall time. logind's own `InhibitDelayMaxSec` (default 5 s) does
**not** apply to `block` inhibitors — it only caps `delay` mode — so no
logind config changes are required.

## Diagnostics

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
