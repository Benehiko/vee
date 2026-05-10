---
title: vee tunnel
weight: 60
---

Forward a port from inside the VM to a random local port via SSH tunneling.

```
vee tunnel <name> <port>
```

Useful for accessing services running inside NAT VMs (e.g. a web server on port 8080) without bridge networking.

## Example

```sh
vee tunnel myvm 8080
# Forwarding 127.0.0.1:54321 -> myvm:8080
```

The forwarded local port is printed and held open until you press Ctrl+C.
