---
title: vee ip
weight: 120
---

Show a running VM's network addresses.

```
vee ip <name>
```

With a guest agent (QEMU VMs created from templates that enable `guest_agent`), prints a table of the guest's interfaces, MACs and addresses.

Without a guest agent, host-visible guests — vz (macOS) VMs and bridge-mode QEMU VMs — are resolved by MAC address from the host's DHCP lease and ARP/neighbour tables, and a bare IP is printed, which keeps the output scriptable:

```sh
ping $(vee ip mymac)
```

User-mode (slirp) QEMU guests are not visible to the host; without a guest agent their IP cannot be shown.
