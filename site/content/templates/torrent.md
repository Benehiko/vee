---
title: torrent
weight: 70
---

Lightweight VM running qbittorrent-nox (headless). Suitable for a dedicated download VM on a NAS or home server.

## Create

```sh
vee create dl --template torrent
```

## Storage

Downloads can be written to a share instead of the VM's own disk, in one of two ways.

### virtiofs (host directory)

Pass host directories interactively, or with `--virtiofs-dir`. The host mounts the
storage and passes the directory through to the guest, so the guest needs no
network access to the NAS:

```sh
vee create dl --template torrent --virtiofs-dir /mnt/nas-nfs/movies
```

### NFS (mounted by the guest)

`--nfs-mount SERVER:EXPORT:GUESTPATH` has the guest mount the export directly.
The flag is repeatable. IPv6 servers must be bracketed, e.g. `[fd00::1]:/export:/downloads`:

```sh
vee create dl --template torrent --nic-mode bridge \
  --nfs-mount 192.168.178.76:/mnt/Data/Movies:/downloads/movies \
  --nfs-mount 192.168.178.76:/mnt/Data/Shows:/downloads/shows
```

Because the guest reaches the server over the LAN, `--nic-mode=bridge` is required —
user-mode NAT cannot route to it.

**NFS traffic always bypasses the VPN.** The server sits on the LAN and is not
reachable through the tunnel, so an outbound exception is created for every NFS
server whether or not a VPN is configured. With a kill-switch enabled this is what
keeps the mounts working; without one the rule is redundant against ufw's default
allow-outgoing policy, but it is still emitted so that hardening the base rules later
cannot silently break every mount. Exceptions are per-server and de-duplicated, so
several exports on one server produce a single rule.

One consequence worth knowing: on a VPN'd VM, torrent traffic goes through the tunnel
while NFS deliberately does not, so the NAS sees the VM's real LAN address.

Mounts are written to `/etc/fstab`, so they survive a reboot. NFS mounts default to
`rw,hard,proto=tcp,timeo=600,retrans=2,_netdev`. `hard` is deliberate: a `soft` mount
returns an I/O error mid-write if the server hiccups, which qBittorrent reports as an
errored torrent rather than retrying.

The first mount's guest path becomes qBittorrent's default save path. In-progress
torrents are written to `/var/lib/qbittorrent/incomplete` on the VM's own disk, and
only the completed file is moved to the save path — this keeps the random small
writes of an active download off the network. Size the VM's disk to fit the largest
set of torrents you expect to have downloading at once.

## Defaults

| Setting | Value |
|---------|-------|
| Memory | 1G |
| CPUs | 2 |
| Network | User-mode NAT |
| Incomplete downloads | `/var/lib/qbittorrent/incomplete` (local disk) |

## Access

The qbittorrent-nox web UI is available at the VM's IP on port 8080. Use `vee tunnel dl 8080` to forward it to localhost.
