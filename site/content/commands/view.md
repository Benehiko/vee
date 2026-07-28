---
title: vee view
weight: 90
---

Open the VM display.

```
vee view <name>
```

For VMs with a SPICE display configured, opens a SPICE client connected to the VM's display port. Useful for initial setup or when SSH is not available.

For GPU passthrough VMs, the display is rendered directly by the passed-through GPU — connect a monitor to the GPU or use a streaming solution like Sunshine/Moonlight.

For **macOS guests** (the `vz` backend) there is no SPICE server: vee resolves the guest's IP by MAC from the host's DHCP leases and opens `vnc://<ip>`, which macOS hands to Screen Sharing. vee first checks that something is listening on port 5900 and explains what to do when nothing is — a freshly restored guest needs a few minutes on its first boot, and macOS gates the screen-sharing agent behind privacy permissions a VM cannot grant itself, so `vee ssh` is often the more reliable route. See [macOS guests](../../getting-started/macos-guests/).
