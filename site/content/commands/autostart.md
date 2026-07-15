---
title: vee autostart
weight: 135
---

Enable or disable autostart for a VM. VMs with autostart enabled are started (and kept running) by the [vee daemon]({{< relref "daemon" >}}).

```
vee autostart <name> [on|off]
```

- With no second argument, prints the current autostart status.
- Pass `on` or `off` to change it.

## Examples

```sh
# Show current setting
vee autostart myvm

# Enable / disable
vee autostart myvm on
vee autostart myvm off
```

Autostart only takes effect while the daemon is running. See [`vee daemon`]({{< relref "daemon" >}}).
