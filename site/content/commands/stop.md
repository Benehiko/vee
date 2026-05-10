---
title: vee stop
weight: 30
---

Stop a running VM with a graceful ACPI shutdown.

```
vee stop <name>
```

Sends an ACPI power-down event to the VM via QMP. The guest OS performs a clean shutdown. If the VM does not stop within the timeout, the QEMU process is killed.
