---
title: vee
geekdocNav: false
geekdocAlign: center
geekdocAnchor: false
---

# vee

A command-line VM manager built on QEMU/KVM. Create, start, stop, SSH into, and monitor virtual machines from a single lightweight tool.

```sh
vee create myvm       # create an Ubuntu 24.04 server VM
vee start myvm        # boot it (detached)
vee ssh myvm          # SSH in
vee stop myvm         # graceful shutdown
```

{{< columns >}}

**Simple setup**

Install from source in one command. Batteries included: cloud images, cloud-init, SSH key management, and SPICE display.

<--->

**GPU passthrough**

Pass a PCIe GPU directly to a VM with VFIO. Built-in preflight checks, IOMMU group validation, and automatic D3cold recovery.

<--->

**Bridge & NAT networking**

User-mode NAT for quick dev VMs. Bridge mode for VMs that need real LAN access (NAS, gaming, Windows).

{{< /columns >}}

{{< button relref="/getting-started/installation/" >}}Get Started{{< /button >}}
