---
title: gaming
weight: 48
---

Legacy alias for [`gaming-arch`]({{< relref "gaming-arch" >}}). Kept for backwards compatibility.

```sh
vee create win-gaming --template gaming --gpu-pci 08:00.0
```

Behaviour is identical to `gaming-arch`, with one convenience: if you pass `--gpu-pci` without `--gpu-mode`, passthrough is enabled implicitly.

Prefer [`gaming-arch`]({{< relref "gaming-arch" >}}) (fresh Arch install) or [`gaming-bazzite`]({{< relref "gaming-bazzite" >}}) (Bazzite ISO) for new VMs. To boot an existing Windows/Linux install off a real NVMe with passthrough, use [`passthrough`]({{< relref "passthrough" >}}).
