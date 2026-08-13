---
title: Linux guests on vz
weight: 46
---

On an Apple Silicon Mac, a **Linux guest** can run on Apple's
Virtualization.framework instead of QEMU — the same `vz` backend that hosts
[macOS guests]({{< relref "macos-guests" >}}), selected per VM with
`--backend vz`.

```sh
vee create dev --template server --distro ubuntu --backend vz --vsock
vee ssh dev
```

Any cloud-image template works unchanged (`server`, `devbox`,
`github-runner`, …) — the template builds exactly as it would for QEMU, and
the backend flag switches the machine that boots it.

## Why

The main reason is **vsock**. The bundled macOS QEMU is a plain-HVF build with
no vsock device, and QEMU's vsock is vhost-only (a Linux/KVM feature), so a
QEMU Linux guest on a macOS host can never have a host↔guest vsock channel.
Virtualization.framework provides one natively (`VZVirtioSocketDevice`), so
`vsock: true` works for Linux guests on macOS hosts — with virtio disks,
networking, and EFI boot as native framework devices alongside.

## What you get

- **EFI boot** from a whole-disk raw image. The template's qcow2 cloud-image
  overlay is flattened to a raw disk once, at create time (`qemu-img` is
  required for that step: `brew install qemu`).
- **cloud-init provisioning** via the normal `cidata.iso` seed.
- **NAT networking** with a deterministic MAC; `vee ssh` and `vee ip` resolve
  the guest address from the host's DHCP leases.
- **A boot console log** — the guest's virtio console is captured to
  `serial.log` in the VM directory.
- **vsock** with `--vsock`, driven through the helper control protocol.

## Limitations

vz Linux guests are **headless** (no SPICE or GPU — use the QEMU backend for
graphical guests), **NAT-only** (no bridge networking, no `ssh_port`
forwarding), and currently have **no virtiofs shares, TPM, raw device
passthrough, or nested virtualization**.

See [docs/linux-vz.md](https://github.com/Benehiko/vee/blob/main/docs/linux-vz.md)
for the full details.
