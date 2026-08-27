---
title: vee wait
weight: 47
---

Block until a running VM is usable over SSH.

```
vee wait <name> [--timeout 10m] [--cloud-init]
```

Succeeds once an authenticated SSH command round-trip to the guest completes (`true` on POSIX guests, `ver` on Windows guests). This is a stronger signal than the boot-time spinner, whose readiness probe only checks that something accepts on the SSH port — which can happen before the guest's authorized keys are in place or first-boot provisioning has run.

With `--cloud-init` it additionally runs `cloud-init status --wait`, so "ready" also means the template's first-boot provisioning is done (POSIX guests only; guests without cloud-init pass trivially).

Exits non-zero when the timeout passes or the VM's process exits, so it chains:

```sh
vee start myvm && vee wait myvm --cloud-init && vee ssh myvm -- docker version
```
