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

## VPN and the kill-switch

A VPN is optional but is the reason most people run this template in a VM. Two
mechanisms are supported, and they enforce the kill-switch in different places.

`vee create` prompts for the VPN interactively — the torrent template has no
`--wg-conf` or `--nordvpn-token` flag of its own (those belong to the
[bitmagnet]({{< relref "bitmagnet" >}}) template). Answer `y` at
`Configure VPN?` and pick a provider:

```text
Configure VPN? [y/N]: y
Provider:
  1) NordVPN (access token)
  2) Generic WireGuard config file
```


### Generic WireGuard

Choose option 2 and give the path to an existing `wg0.conf` from your provider.
The guest gets a `ufw` kill-switch: the default outbound policy is deny, and the
only unrestricted egress is the tunnel device itself.

The exceptions punched through the deny policy are deliberately narrow:

| Hole | Scope | Why |
|------|-------|-----|
| `allow out on wg0` | the tunnel device | The only unrestricted egress. It exists only while the tunnel is up. |
| `allow out on lo` | loopback | Local services, including the web UI that `vee tunnel` reaches over SSH. |
| `allow out 22/tcp` | outbound SSH | The outbound half of an inbound SSH session leaves on the LAN interface, not `wg0`, so without this the replies are dropped and the guest becomes unreachable. |
| handshake hole | the endpoint's resolved addresses, on its UDP port only | Pinned to those addresses rather than opened to the whole internet on that port — an unpinned rule would let any process reach any host listening there with the tunnel down. |
| NFS servers | one rule per server | NFS is on the LAN and is not reachable through the tunnel. See above. |

Everything else stays inside the tunnel. qBittorrent's own port is never opened.

**The endpoint is resolved before the deny policy takes effect** and the
addresses are written to `/etc/wireguard/endpoint-addrs`. This matters because
once outbound traffic is denied there is no DNS left to resolve a hostname
endpoint with — so the lookup happens while it still can, and later boots read
the answer back from disk. A config naming a hostname works as a result, but an
endpoint given as a literal IP is still the more robust choice: a baked address
goes stale if your provider re-addresses the server.

**A tunnel that fails at boot retries itself.** `wg-quick@wg0` is enabled, so
systemd starts it on every boot, but the upstream unit sets no `Restart=` and
`ufw` restores the deny policy earlier in boot than `wg-quick` runs. A handshake
that fails at that moment would otherwise never be retried, leaving the guest
firewalled with no tunnel until someone opened a console. A `vee-wg-retry.timer`
re-attempts it every 60s until the tunnel holds.

That failure mode is safe but silent: it fails *closed*, so nothing leaks, and
the VM looks healthy while downloading nothing. Check it with `vee network dl`.

### NordVPN

Choose option 1 and supply an access token (and optionally a country). The
template installs the NordVPN snap and connects over NordLynx. Here the
kill-switch is enforced by the NordVPN daemon rather than by `ufw`, so the
exceptions above do not apply — NFS servers and port 22 are registered with
`nordvpn whitelist` instead.

### Verifying

`vee network dl` reports the tunnel state, the firewall policy, and the guest's
egress IP. The egress IP is the one that matters: if it equals your home
address, traffic is leaving outside the tunnel.

## Defaults

| Setting | Value |
|---------|-------|
| Memory | 2G |
| CPUs | 1 |
| Network | User-mode NAT |
| Incomplete downloads | `/var/lib/qbittorrent/incomplete` (local disk) |

## Access

The qbittorrent-nox web UI listens on port 8080 inside the guest. The default
user-mode NAT network forwards it to `127.0.0.1:8080` on the host; on a bridged
VM, or any VM with a VPN kill-switch, use `vee tunnel dl 8080` to forward it over
SSH instead.

With a kill-switch enabled the port is never opened to the LAN, so `vee tunnel`
is the only way in.
