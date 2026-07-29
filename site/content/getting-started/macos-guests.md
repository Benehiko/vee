---
title: macOS guests
weight: 45
---

vee can run **macOS guests** on Apple Silicon hosts using Apple's Virtualization.framework (the `vz` backend). QEMU remains the backend for every other guest.

```sh
vee create mymac --template macos      # restore the newest macOS this host supports, then start it
vee ssh mymac                          # works on first boot — no GUI setup needed
vee view mymac                         # the guest's screen over Screen Sharing (works from its second boot)
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

The guest's login password defaults to the account name (`vee`), because you type it at the guest's login window and at every Screen Sharing prompt. Pass `--password` for something stronger — worthwhile if the guest is long-lived, since Remote Login is enabled. Either way it is printed once and saved to `macos-credentials.txt` in the VM directory (mode 0600). SSH uses your vee key, not the password.

### Importing an existing guest

If you already restored a macOS VM with [macosvm](https://github.com/s-u/macosvm), import it instead of restoring again:

```sh
vee create mymac --template macos --macosvm-dir ~/my-macosvm-vm
```

vee copies the disk and auxiliary storage and reads the hardware-model and machine-identifier blobs out of `macosvm.json`. Imported guests are left exactly as they are — vee does not re-provision an installation someone else set up. It also writes no privacy grants for such a guest, so `vee view` will not work until you enable Screen Sharing, and its screen-recording permission, inside the guest yourself.

### Opting out of provisioning

`--skip-first-boot` leaves the restored guest untouched. It will boot into Setup Assistant, which you must complete at its screen; `vee ssh` will not work until you enable Remote Login inside the guest, and `vee view` will not work until you enable Screen Sharing and its screen-recording permission there too.

## Stopping a guest

`vee stop` asks the guest to run `shutdown -h now` over SSH, then also sends the framework's stop request, and finally kills the VM if it has not exited. The SSH step exists because a macOS guest ignores `requestStop` — the ACPI-powerdown analog other guests honour — so without it every stop waited out its grace period and then killed the VM, which is an unclean shutdown of a live filesystem.

Provisioning installs a `sudoers` rule granting the guest account exactly one privileged command, `/sbin/shutdown`, so no password is needed. A guest that was imported or created with `--skip-first-boot` has no such rule and still falls back to being killed after the grace period.

## Terminal type

A macOS guest has only the terminfo database Apple ships, which covers `xterm*`, `screen*`, `tmux*` and friends but not the entries modern emulators use — Ghostty sends `TERM=xterm-ghostty`, kitty `xterm-kitty`. Left alone the guest cannot resolve your terminal and zsh's line editor garbles input. `vee ssh` detects that and uses `xterm-256color` for the session, printing a one-line note. QEMU guests are untouched, since their distros ship a broader database.

## Networking

Virtualization.framework NAT has no host port forwarding, so macOS guests get no `127.0.0.1` SSH port. vee resolves the guest's address by MAC from the host's DHCP lease database instead — `vee ssh`, `vee ip` and `vee view` all use it. The guest is reachable from the host, not from the wider LAN.

## What works, and what does not

| Feature | macOS guest (`vz`) |
|---|---|
| create / start / stop / delete / list / status | Works |
| `vee ssh`, `vee ip` | Works (IP resolved by MAC from host DHCP leases) |
| `vee view` | Works via the guest's Screen Sharing service |
| Graceful shutdown | Works — vee asks the guest to run `shutdown -h now` over SSH, because macOS ignores the framework's stop request |
| Guest-shutdown detection, autostart | Works |
| `vee logs` | Helper log (`vz-helper.log`) rather than a QEMU log |
| SPICE, `vee monitor`, `vee qmp`, dashboard stats | Not applicable — no SPICE server and no QMP on this backend |
| `vee check`, `vee ports` | Unavailable (they need the QEMU guest agent) |
| Snapshots, `vee move`, virtiofs shares | Not implemented for this backend yet |
| Suspend / resume | Not implemented yet (Virtualization.framework supports it on macOS 14+) |

## Known caveats

- **Screen Sharing needs no manual step on a guest vee provisioned, but it starts working on that guest's second boot.** Enabling the service is not enough: since macOS 12.1 the screen-sharing agent also needs privacy (TCC) permissions, which macOS only offers through System Settings or MDM — neither reachable on a headless guest, and ARD's `kickstart` says as much (*"must be enabled from System Settings or via MDM"*). So vee grants them directly in the guest's privacy database, which SIP protects while the guest runs but not while its disk sits idle on the host. macOS creates that database on the guest's *first* boot rather than during the restore, so `screen_sharing_grant_pending` in the config records that vee still owes the guest its grants, and every `vee start` retries until they land — after which the flag is cleared and later starts do not touch the disk at all. So the guest has to be stopped and started once: `vee create` restores and boots it, `vee ssh` works immediately, and the next `vee start` after a `vee stop` writes the grants, so `vee view` works from that boot onwards. Only three rows for that one client are ever touched, they are removed again if provisioning is rolled back, and nothing is written when Screen Sharing is not requested — including for imported and `--skip-first-boot` guests, which vee never provisions. `vee view` still reports when nothing is listening at all.
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
  screen_sharing_grant_pending: true   # vee still owes this guest its Screen Sharing grants
```

vee raises a guest's CPU count and memory to `min_cpus` / `min_memory_bytes` — both when the config is written and again at every start, since a guest below its restore image's requirements will not boot. Your `vm.yaml` is left as you wrote it; the clamp is applied to what the VM is actually given.

The helper is ad-hoc codesigned with `com.apple.security.virtualization`, which macOS honours without a paid Apple Developer account. If you downloaded the release tarball with a browser, macOS quarantines the helper, which Gatekeeper would then refuse to run; vee clears that flag (re-signing only if the signature is missing the entitlement) every time it resolves the helper, so the restore and start paths are both covered.

QEMU also has an experimental macOS-guest path (`vmapple` + `apple-gfx`), which vee does **not** use: upstream QEMU compiles that machine out by default and it does not run on current macOS hosts. It is tracked in [issue #50](https://github.com/Benehiko/vee/issues/50) and the seam for it is still in place.
