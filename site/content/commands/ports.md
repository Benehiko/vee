---
title: vee ports
weight: 70
---

List bound TCP ports inside a running VM using the QEMU guest agent.

```
vee ports <name>
```

Requires `qemu-guest-agent` installed inside the guest and `guest_agent: true` in `vm.yaml`.

## Example

```
PORT   PROTO  STATE
22     tcp    LISTEN
80     tcp    LISTEN
443    tcp    LISTEN
```
