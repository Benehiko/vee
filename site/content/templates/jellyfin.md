---
title: jellyfin
weight: 75
---

Ubuntu cloud image running [Jellyfin](https://jellyfin.org) installed from the upstream APT repository. Libraries are attached via repeatable `--media` flags that abstract over NFS, SMB, local host directories, raw block devices, and USB drives. Avahi publishes the VM's hostname over multicast DNS so the UI is reachable at `http://<vm-name>.local` (and on many client setups, simply `http://<vm-name>`) from anywhere on the LAN.

## Create

A Jellyfin VM with two NFS libraries pulled from a TrueNAS server:

```sh
vee create jellyfin --template jellyfin --nic-mode bridge \
  --media nfs://truenas.lan/mnt/Data/Movies@/media/movies \
  --media nfs://truenas.lan/mnt/Data/Shows@/media/shows
```

Bridge networking is required — the template refuses `--nic-mode=user` because multicast DNS and Jellyfin's auto-discovery cannot traverse QEMU user-mode NAT.

## Defaults

| Setting | Value |
|---------|-------|
| Memory | 1G |
| CPUs | 2 (1 core, 2 threads / SMT) |
| Network | Bridge (`br0`) |
| Display | Headless |
| Firewall | `ufw` open on 8096/tcp, 8920/tcp, 1900/udp, 7359/udp, 5353/udp |
| Hostname | VM name, published via `avahi-daemon` |

## `--media` source syntax

`--media` is repeatable. The general form is `<kind>:<source>@<guest-path>[:<suffix>]`. The optional suffix is either `ro` for a read-only mount or, for `block:` and `usb:` sources, the filesystem type to mount the device as.

| Kind | Example | Notes |
|------|---------|-------|
| Host directory | `hostdir:/mnt/4TB/photos@/media/photos` | Shared via virtiofs. The guest mounts the tag on first boot. |
| NFSv4 | `nfs://truenas.lan/mnt/Data/Movies@/media/movies` | Installs `nfs-common`. Mount is wired via a systemd `.automount` unit, so it tolerates the NFS server flapping or coming up after the VM. |
| SMB / CIFS | `smb://alice@nas.lan/Music@/media/music` | Installs `cifs-utils`. The CLI prompts for the password at create time and bakes it into the cloud-init cidata ISO with `0600` permissions. The password is never stored in the VM's `vm.yaml`. |
| Block device | `block:/dev/disk/by-id/ata-ST2000DM008@/media/scratch:ext4` | Raw passthrough as a virtio-blk-pci device. The optional `:fstype` suffix tells the guest to mount the device automatically. |
| USB | `usb:0951:1666@/media/usb:ext4` | Pass through a USB device by `vendorid:productid` (or `usb:bus=N,addr=M@/...`). The optional `:fstype` suffix mounts the first USB block device automatically. |

Multiple sources can be mixed in one command; they are merged into the VM's cloud-init in order.

## NFS desync recovery

Earlier deployments often layered a `bindfs` FUSE remount over a host-side NFS mount to fix permissions. When the NFS export auto-unmounted, the FUSE layer would not re-attach to the fresh underlying mount and the consumer service would silently lose access to its files.

The jellyfin template avoids that class of failure entirely by performing the mount inside the guest with a systemd `.automount` unit (`After=network-online.target`, `TimeoutIdleSec=600`). The mount is re-established lazily on first access after a server-side flap, and permissions are governed by NFS export configuration plus the guest's local jellyfin user — no FUSE remap is needed.

## SMB credentials

SMB passwords are collected interactively when `vee create` runs and written once into the cloud-init cidata ISO as `/etc/cifs-credentials-<guest-path>` with `0600` permissions. They are persisted inside the guest filesystem after first boot, but never appear in `vm.yaml` on the host or in any vee-managed log. Re-creating the VM re-prompts for the password.

## Hostname publishing

`avahi-daemon` is installed and enabled on first boot, advertising the VM's hostname (which defaults to the VM name) over UDP 5353. From any LAN client that supports mDNS — most Linux distributions, macOS, recent Windows builds — `http://<vm-name>.local` resolves to the VM's bridge IP. Whether `http://<vm-name>` (without `.local`) works depends on the client's DNS search domains, which is outside vee's control.

## Access

```sh
vee tunnel jellyfin 8096   # forward Jellyfin UI to localhost:8096
vee ssh jellyfin
```
