---
title: vee stop
weight: 30
---

Stop a running VM with a graceful ACPI shutdown.

```
vee stop <name>
```

Sends an ACPI power-down event to the VM via QMP. The guest OS performs a clean shutdown. If the VM does not stop within the timeout, the QEMU process is killed.

## Guest-initiated shutdown and reboot

`poweroff` inside the guest stops the VM and parks it — the daemon will not restart it.

`reboot` inside the guest power-cycles the VM: vee detects the guest-requested reset and relaunches a fresh QEMU process. QEMU's in-place warm reset is not used on purpose — under HVF the firmware can wedge on re-entry (dead display, SSH connection reset) while the process still looks healthy. Reboots an installer performs during a pending install pass (Windows setup) are left in place.
