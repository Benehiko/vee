---
title: vee wait
weight: 47
---

Block until a running VM is usable over SSH.

```
vee wait <name> [--timeout 10m] [--cloud-init]
```

Succeeds once an authenticated SSH command round-trip to the guest completes (`true` on POSIX guests, `ver` on Windows guests). `vee start`'s boot wait uses the same probe for provisioned guests; `wait` covers what start cannot: a VM that is already running (started earlier, by the daemon, or from another terminal), and gating on first-boot provisioning with `--cloud-init`.

With `--cloud-init` it additionally runs `cloud-init status --wait`, so "ready" also means the template's first-boot provisioning is done (POSIX guests only; guests without cloud-init pass trivially).

Exits non-zero when the timeout passes or the VM's process exits, so it chains:

```sh
vee start myvm && vee wait myvm --cloud-init && vee ssh myvm -- docker version
```
