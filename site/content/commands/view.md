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
