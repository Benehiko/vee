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

## Base OS

The default base is Ubuntu: qBittorrent runs as a systemd unit and the
kill-switch is enforced with `ufw`.

`--distro alpine` selects a much smaller Alpine guest that enforces the same
policy with iptables under OpenRC:

```sh
vee create dl --template torrent --distro alpine
```

The Alpine base is the better-hardened of the two. Its inbound policy is
default-drop rather than rule-by-rule, and two of the outbound holes are
narrower than ufw's rule vocabulary can express — the SSH hole is restricted to
connections that are already established (a bare source-port rule would let any
process open new connections to the internet with the tunnel down), and DHCP
renewal is pinned to the broadcast address.

It costs two things:

| | Ubuntu (default) | Alpine (`--distro alpine`) |
|---|---|---|
| Architecture | x86_64 and arm64 | x86_64 only |
| SPICE display | Yes | No — headless; the web UI is reached over `vee tunnel` |
| Memory | 2G | 1G |
| Firewall | `ufw` | `iptables` |
| NordVPN | NordVPN client (daemon kill-switch) | NordLynx config (firewall kill-switch) |

**A NordVPN account works on both bases.** The NordVPN client is a snap and
Alpine has no snapd, but NordLynx is WireGuard: on the Alpine base your account
token is exchanged for a NordLynx `wg0.conf` and drives the same firewall
kill-switch as any other WireGuard config. The prompt is identical either way.
The difference is only in who enforces the kill-switch — the NordVPN daemon on
Ubuntu, the firewall on Alpine.

Existing Ubuntu VMs are unaffected — the base is chosen at create time and
nothing migrates.

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

The exceptions punched through the deny policy are deliberately narrow. The
table below names the `ufw` rules of the default Ubuntu base; the Alpine base
applies the same policy with iptables, and narrows two of them further (see
[Base OS](#base-os)):

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
the answer back from disk.

**A hostname endpoint is re-resolved when its address changes.** Pinning the
hole to addresses resolved once would strand the guest the day a provider
re-addresses its server: `wg-quick` would dial the new IP while the firewall
still permitted only the old one, and the tunnel would never come back — failing
closed, so nothing leaks, but silently and across reboots. Guests configured
with a hostname therefore carry `/usr/local/sbin/vee-wg-refresh-endpoint`, which
re-resolves the endpoint, re-pins the hole to whatever comes back, and restarts
the tunnel so it re-reads the address `wg-quick` froze when the interface came
up. On the Ubuntu base it runs from the existing retry timer; on Alpine it runs
from the boot hook and `crond`.

The refresh fails closed at every step. The lookup needs DNS, which the deny
policy blocks, so it opens a hole to the configured nameservers on port 53 and
closes it again immediately — on the failure path too, never leaving it open. If
resolution fails or returns nothing, the addresses already pinned are left
exactly as they are: a stale rule keeps a working tunnel working, whereas
clearing the rules first would leave a window with no kill-switch at all. New
holes are installed before superseded ones are withdrawn, so the handshake is
never without a way out.

An endpoint given as a literal IP skips all of this — an address that cannot
change needs no refresh — and remains the simplest choice where your provider
offers one.

**A tunnel that fails at boot retries itself.** `wg-quick@wg0` is enabled, so
systemd starts it on every boot, but the upstream unit sets no `Restart=` and
`ufw` restores the deny policy earlier in boot than `wg-quick` runs. A handshake
that fails at that moment would otherwise never be retried, leaving the guest
firewalled with no tunnel until someone opened a console. A `vee-wg-retry.timer`
re-attempts it every 60s until the tunnel holds.

That failure mode is safe but silent: it fails *closed*, so nothing leaks, and
the VM looks healthy while downloading nothing. Check it with `vee network dl`.

### NordVPN

Choose option 1 and supply an access token (and optionally a country).

On the default Ubuntu base the template installs the NordVPN snap and connects
over NordLynx. The kill-switch is enforced by the NordVPN daemon rather than by
`ufw`, so the exceptions above do not apply — NFS servers and port 22 are
registered with `nordvpn whitelist` instead.

On the Alpine base the token is exchanged for a NordLynx WireGuard config up
front, and from there it is an ordinary WireGuard tunnel behind the firewall
kill-switch — the exception table above applies unchanged. `vee network` reports
the provider as `nordlynx`, since what the guest actually runs is a `wg0`
interface rather than the NordVPN daemon.

### Verifying

`vee network dl` reports the tunnel state, the firewall policy, and the guest's
egress IP. The egress IP is the one that matters: if it equals your home
address, traffic is leaving outside the tunnel.

## Defaults

Values for the default Ubuntu base; see [Base OS](#base-os) for where the
Alpine base differs.

| Setting | Value |
|---------|-------|
| Memory | 2G |
| CPUs | 1 |
| Network | User-mode NAT |
| Incomplete downloads | `/var/lib/qbittorrent/incomplete` (local disk) |

## Access

The qbittorrent-nox web UI listens on port 8080 inside the guest, and the guest
firewall never opens that port to the LAN — on any torrent VM, with or without a
VPN. `vee tunnel` is the only way in.

On the default user-mode NAT network the port is forwarded to `127.0.0.1:8080`
on the host, which reaches the guest over loopback and works out of the box. On
a bridged VM there is no such forward, so use `vee tunnel dl qbittorrent` (or
`vee tunnel dl 8080`) to forward it over SSH. `vee tunnel` detects a port it
cannot reach directly and falls back to SSH on its own.

That loopback path is also what makes the UI usable without a password.
qBittorrent is configured with `LocalHostAuth=false`, so it skips authentication
for loopback connections only; a request arriving from the LAN would be answered
with `403` even if the firewall let it through. Reaching the UI therefore means
having SSH access to the guest, which is the intended access model — the same
one the Alpine base and the [bitmagnet]({{< relref "bitmagnet" >}}) template use.
