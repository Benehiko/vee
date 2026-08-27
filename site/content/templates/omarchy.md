---
title: omarchy
weight: 26
---

[Omarchy](https://omarchy.org/) — DHH's opinionated Arch + Hyprland desktop. The install is **fully unattended**: alongside the install ISO, vee attaches a second `cidata`-labelled seed ISO carrying the answers Omarchy's own wizard would have written (the [autoinstall mechanism](https://omarchy.org/manual/unattended-installs/) Omarchy documents for VMs and fleet machines). The installer partitions the disk, installs, and reboots into the finished desktop on its own — no keyboard required. vee ejects both ISOs once the installed system boots from disk.

Because the seed includes your vee SSH keys as `authorized_keys`, the installer enables `sshd` and opens the firewall for it, so `vee ssh` works from the first boot.

## Create

```sh
vee create omarchy --template omarchy

# choose the login account (defaults: user omarchy, password matching the username)
vee create omarchy --template omarchy --user dev --password secret

# pin a specific ISO release
vee create omarchy --template omarchy --distro-version 4.0.1
```

Omarchy is also available as a distro on the cloud-init templates that make sense for it:

```sh
vee create dev --template devbox --distro omarchy    # SSH user "dev"
vee create hypr --template desktop --distro omarchy
```

These delegate to the same unattended installer — Omarchy has no cloud image and no cloud-init, but its stock install already includes Docker, lazydocker, git, and the rest of the dev tooling a devbox provisions elsewhere.

The ISO can be fetched ahead of time:

```sh
vee pull omarchy
```

## Defaults

| Setting | Value |
|---------|-------|
| Memory | 8G |
| CPUs | 4 |
| Disk | 60G qcow2 (2GiB ESP + btrfs with `@/@home/@log/@pkg` subvolumes) |
| Network | User-mode NIC, SSH port forwarded |
| Display | virtio-gpu GL (virgl) — Hyprland needs GL |
| Login | user `omarchy` (password matches the username unless `--password` is given) |
| Guest agent | Enabled |
| UEFI | Yes |

## Notes

- The password is seeded into the installer as a SHA-512 crypt hash (the same `openssl passwd -6` format Omarchy's wizard writes); the plain text never lands on the seed. The default password (matching the username) is for the graphical console — change it in the guest for anything reachable from untrusted networks.
- The seed's `user_configuration.json` follows the archinstall schema of the pinned ISO releases. The Omarchy mirror keeps only the current release, so `--distro-version` accepts the releases vee knows about; `latest` (the default) resolves to the newest.
- x86_64 hosts only — Omarchy publishes no arm64 (aarch64) ISO, so the template is refused on Apple Silicon.
- Disk encryption is deliberately not seeded: an encrypted autoinstall still stops at the LUKS passphrase on every boot, which defeats an unattended VM.

For a cloud-init managed GNOME desktop instead, use [`desktop`]({{< relref "desktop" >}}).
