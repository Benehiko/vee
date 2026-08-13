# Linux guests on Virtualization.framework (vz backend)

On an Apple Silicon Mac, vee can run Linux guests on Apple's
**Virtualization.framework** instead of QEMU — the same `vz` backend that runs
[macOS guests](macos.md#macos-guests-virtualizationframework), extended with a
Linux platform (issue #127).

```sh
vee create dev --template server --distro ubuntu --backend vz --vsock
vee ssh dev
```

Any cloud-image template works the same way (`server`, `devbox`,
`github-runner`, …): the template is built exactly as it would be for QEMU,
and `--backend vz` switches the machine that boots it.

## Why choose vz over QEMU for a Linux guest

- **A working host↔guest vsock device.** This is the headline reason. The
  bundled macOS QEMU is a plain-HVF build with no vsock device, and QEMU's
  vsock is vhost-only — a Linux/KVM feature — so a QEMU Linux guest on a macOS
  host can never have one. Virtualization.framework provides
  `VZVirtioSocketDevice` natively: create the VM with `vsock: true` and the
  whole vsock toolchain from issue #121 (`vsock-connect` / `vsock-listen`
  control ops, `Manager.VZVsockConnect` / `VZVsockListen`) works for Linux
  guests with no vhost dependency.
- **First-class Apple silicon virtualization** — virtio-blk, virtio-net,
  virtio-rng and the EFI boot loader are all native framework devices; there
  is no QEMU device model in between.

## What a vz Linux guest looks like

| Aspect | Behavior |
|---|---|
| Boot | EFI (`VZEFIBootLoader`) from a whole-disk **raw** image; the per-VM NVRAM lives in `efi-vars.fd` inside the VM directory |
| Boot disk | The template's qcow2 cloud-image overlay is flattened to a raw image once, at create time, with `qemu-img convert` (then grown to the configured size — cloud-init's growpart claims the space on first boot) |
| Provisioning | The normal cloud-init `cidata.iso` seed, attached as a read-only virtio-blk device |
| Networking | NAT (`VZNATNetworkDeviceAttachment`) with vee's deterministic MAC. `vee ssh` / `vee ip` resolve the guest IP from the host DHCP leases by MAC, exactly like macOS guests |
| Display | None — vz Linux guests are headless; use `vee ssh` |
| Console | The guest's virtio console is captured to `serial.log` in the VM directory (truncated on each boot) — the place to look when a guest does not come up |
| vsock | `vsock: true` attaches a `VZVirtioSocketDevice`, driven through the helper control protocol |
| Shutdown | `vee stop` asks the guest to power off over SSH, then sends the framework's stop request; systemd handles the latter like a power button |

The guest is hosted by the same `vee-vz-helper` process that hosts macOS
guests — the machine spec (`vz-machine.json`) carries `"platform": "linux"`
and the helper builds a `VZGenericPlatformConfiguration` instead of the mac
platform. The helper ships in the `darwin-arm64` release tarball, or build it
from a checkout with `make vz-helper`.

## Requirements and limitations

- **Apple Silicon macOS host** (like every vz VM).
- **`qemu-img`** is needed once, at create time, to flatten the cloud image to
  raw (`brew install qemu`, or the vee-managed QEMU bundle in `~/.vee/bin`).
- **NAT only.** Bridge networking is not wired for the vz backend; templates
  that require a bridge (`jellyfin`, `dns-sink`) are refused at create.
- **No host port-forwarding.** `ssh_port` is dropped from the config —
  `vee ssh` reaches the guest by its NAT address instead.
- **Headless.** SPICE and GPU modes are QEMU features; a template's SPICE
  display is dropped (with a log notice), and `--gpu-mode virtio` or
  `passthrough` is refused. Use the QEMU backend for graphical Linux guests.
- **No virtiofs shares, TPM, or raw device passthrough** on the vz backend
  yet. virtiofs and the Rosetta directory share are natural follow-ups —
  Virtualization.framework supports both.
- **Nested virtualization is unavailable** — the framework does not expose
  EL2 to guests. Use the QEMU backend (`--nested`) for that.

## vsock quick tour

```sh
vee create dev --template server --distro ubuntu --backend vz --vsock
```

Host→guest: have the guest listen on a vsock port (e.g. with
`socat VSOCK-LISTEN:5000,fork -` or an AF_VSOCK-aware service), then bridge it
to a host unix socket through the helper — `Manager.VZVsockConnect(ctx, "dev",
5000)` returns a live `net.Conn`.

Guest→host: `Manager.VZVsockListen(ctx, "dev", 5000, "/path/host.sock")`
forwards every guest connection to vsock port 5000 (CID 2, the host) into the
unix socket at that path.

On a Linux host nothing changes: QEMU/KVM already provides vhost-vsock, and
the QEMU backend remains the default there.

## Troubleshooting

- **Guest never gets an IP / `vee ssh` fails** — read `serial.log` in the VM
  directory (`~/.vee/vms/<name>/serial.log`): it carries the kernel and
  cloud-init console output. `vz-helper.log` in the same directory is the
  helper process log (`vee logs <name>` prints it).
- **"the vz backend supports raw disk images only"** — the config points at a
  qcow2 that was never materialized (e.g. a hand-edited `vm.yaml`). Recreate
  the VM, or convert the image yourself with
  `qemu-img convert -O raw disk.qcow2 disk.raw` and point the config at it.
- **The guest must be EFI-bootable.** All of vee's aarch64 cloud images
  (Ubuntu, Fedora, Arch) are. A BIOS-only x86 image cannot boot here.
