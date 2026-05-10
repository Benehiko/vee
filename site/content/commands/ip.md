---
title: vee ip
weight: 120
---

Print the IP address of a running VM.

```
vee ip <name>
```

Uses the same resolution order as `vee ssh`: guest agent first, then ARP/neighbour table, then NAT port. Useful for scripting:

```sh
ping $(vee ip myvm)
```
