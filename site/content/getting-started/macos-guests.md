---
title: macOS guests
weight: 45
---

vee can run **macOS guests** on Apple Silicon hosts using Apple's Virtualization.framework (the `vz` backend). QEMU remains the backend for every other guest.

```sh
vee create mymac --template macos      # restore the newest macOS this host supports, then start it
vee ssh mymac                          # works on first boot — no GUI setup needed
vee view mymac                         # the guest's screen over Screen Sharing
```

`vee create` starts the guest when it finishes; pass `--no-start` if you would
rather start it yourself later with `vee start mymac`.

## Requirements

- **Apple Silicon Mac** running macOS 13 or newer. macOS guests are arm64-only, and Virtualization.framework will not restore a guest newer than the host.
- The **`vee-vz-helper`** binary, which owns each guest VM. It ships in the `darwin-arm64` release tarball next to `vee`; from a source checkout, run `make vz-helper`.
- Disk space for the restore image (**15-20 GB** IPSW depending on the macOS version — 26.6 is 19.8 GB — cached under `~/.vee/iso/`) plus the guest disk (64 GB by default, sparse).
- `sudo` once per guest, during creation. vee explains why when it asks: launchd only loads root-owned daemons, and the first-boot provisioning it installs into the guest is a launch daemon.

## Licensing

Apple's macOS software licence agreement permits running macOS in a virtual machine **only on Apple-branded hardware**, and at most **two** virtualized copies at a time. Virtualization.framework enforces the two-VM limit itself: starting a third macOS guest fails. The IPSW download itself is public and needs no Apple ID.

## How a guest is created

1. **Restore image.** `--ipsw latest` (the default) asks the host's Virtualization.framework for the newest restore image *this host* can install — more accurate than a global "latest". Pass an `https://…ipsw` URL or a local path for a specific version; [ipsw.me](https://ipsw.me/product/Mac/) and [appledb.dev](https://appledb.dev) index older releases. Pre-fetch with `vee pull macos`.
2. **Install.** `vee-vz-helper` runs `VZMacOSInstaller` against a fresh sparse disk. This takes tens of minutes and prints progress; the guest's CPU and memory are raised to the image's minimums automatically.
3. **Provision.** Before the guest ever boots, vee patches its disk offline: it marks Setup Assistant as already done and installs a first-boot launch daemon. That daemon, on the guest's first boot, creates the admin account, authorizes your vee SSH key, and enables Remote Login and Screen Sharing — which is what makes `vee ssh` work without a GUI session.
4. **Run.** `vee start` launches a `vee-vz-helper` process that owns the VM for its lifetime, exactly as a `qemu-system` process does for a QEMU VM.

The generated GUI login password is printed once and saved to `macos-credentials.txt` in the VM directory (mode 0600). SSH uses your vee key, not the password.

### Importing an existing guest

If you already restored a macOS VM with [macosvm](https://github.com/s-u/macosvm), import it instead of restoring again:

```sh
vee create mymac --template macos --macosvm-dir ~/my-macosvm-vm
```

vee copies the disk and auxiliary storage and reads the hardware-model and machine-identifier blobs out of `macosvm.json`. Imported guests are left exactly as they are — vee does not re-provision an installation someone else set up.

### Opting out of provisioning

`--skip-first-boot` leaves the restored guest untouched. It will boot into Setup Assistant, which you must complete at its screen; `vee ssh` will not work until you enable Remote Login inside the guest.

## Networking

Virtualization.framework NAT has no host port forwarding, so macOS guests get no `127.0.0.1` SSH port. vee resolves the guest's address by MAC from the host's DHCP lease database instead — `vee ssh`, `vee ip` and `vee view` all use it. The guest is reachable from the host, not from the wider LAN.

## What works, and what does not

| Feature | macOS guest (`vz`) |
|---|---|
| create / start / stop / delete / list / status | Works |
| `vee ssh`, `vee ip` | Works (IP resolved by MAC from host DHCP leases) |
| `vee view` | Works via the guest's Screen Sharing service |
| Graceful shutdown | Works (`requestStop`, the ACPI-powerdown analog) |
| Guest-shutdown detection, autostart | Works |
| `vee logs` | Helper log (`vz-helper.log`) rather than a QEMU log |
| SPICE, `vee monitor`, `vee qmp`, dashboard stats | Not applicable — no SPICE server and no QMP on this backend |
| `vee check`, `vee ports` | Unavailable (they need the QEMU guest agent) |
| Snapshots, `vee move`, virtiofs shares | Not implemented for this backend yet |
| Suspend / resume | Not implemented yet (Virtualization.framework supports it on macOS 14+) |

## Known caveats

- **Screen Sharing may refuse connections.** Since macOS 12.1 the screen-sharing agent needs privacy (TCC) permissions that only device management can grant — a VM cannot grant them to itself. `vee view` tells you when nothing is listening. SSH is the reliable route; the guest's own screen is the fallback.
- **A per-user setup wizard may still appear** at the first *graphical* login (Accessibility, Siri, Apple Pay screens). Skipping it entirely needs version-stamped preference files vee does not write yet. SSH is unaffected.
- **The provisioned account has no secure token**, because it is created by a launch daemon rather than by Setup Assistant. FileVault, startup-security changes and in-place macOS upgrades are therefore unavailable in the guest. Restore from a newer IPSW instead of upgrading.
- **The guest cannot run VMs of its own.** Virtualization.framework exposes nested virtualization only to *Linux* guests (`VZGenericPlatformConfiguration`); the macOS-guest platform has no such switch, and Apple states plainly that neither Virtualization.framework nor Hypervisor.framework works from inside a VM. So Docker Desktop, Podman Machine, Lima, UTM and anything else needing a hypervisor will not start in a macOS guest — Docker Desktop fails its hypervisor check outright. Run those on the host, or in a sibling Linux guest.
- **No Apple ID / App Store** sign-in in a VM, and iCloud works only when host and guest are both recent enough.
- Guests older than the host are fine; newer than the host is not. Some Apple Silicon generations also refuse older guests (an M4 host will not boot a guest older than macOS 13.4).

## Under the hood

Backend selection is a per-VM field, orthogonal to the guest OS:

```yaml
backend: vz          # empty or "qemu" selects QEMU
macos:
  auxiliary_storage: /path/to/aux.img
  hardware_model: !!binary "…"        # opaque Virtualization.framework blobs
  machine_identifier: !!binary "…"
  min_cpus: 2
  min_memory_bytes: 4294967296
```

vee raises a guest's CPU count and memory to `min_cpus` / `min_memory_bytes` — both when the config is written and again at every start, since a guest below its restore image's requirements will not boot. Your `vm.yaml` is left as you wrote it; the clamp is applied to what the VM is actually given.

The helper is ad-hoc codesigned with `com.apple.security.virtualization`, which macOS honours without a paid Apple Developer account. If you downloaded the release tarball with a browser, macOS quarantines the helper, which Gatekeeper would then refuse to run; vee clears that flag (re-signing only if the signature is missing the entitlement) every time it resolves the helper, so the restore and start paths are both covered.

QEMU also has an experimental macOS-guest path (`vmapple` + `apple-gfx`), which vee does **not** use: upstream QEMU compiles that machine out by default and it does not run on current macOS hosts. It is tracked in [issue #50](https://github.com/Benehiko/vee/issues/50) and the seam for it is still in place.
