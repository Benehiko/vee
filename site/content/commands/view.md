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

For **macOS guests** (the `vz` backend) there is no SPICE server: vee resolves the guest's IP by MAC from the host's DHCP leases and opens `vnc://<ip>`, which macOS hands to Screen Sharing. vee first checks that something is listening on port 5900 and explains what to do when nothing is — a freshly restored guest needs a few minutes on its first boot. macOS also gates the screen-sharing agent behind privacy permissions that a guest cannot grant itself. For a guest vee provisioned, vee writes those into the guest's disk while it is stopped — which needs one restart after the guest's first boot, and `vee create` performs that restart itself, so Screen Sharing is ready when create returns. An imported or `--skip-first-boot` guest gets no grants, and has to be configured inside the guest. See [macOS guests](../../getting-started/macos-guests/).
