---
title: bitmagnet
weight: 78
---

A minimal Alpine Linux VM running [bitmagnet](https://bitmagnet.io/) — a self-hosted BitTorrent DHT crawler and indexer — with its own PostgreSQL database, behind a WireGuard kill-switch.

The VM is deliberately small: 2 GB of RAM, two vCPUs, and an 8 GB disk. bitmagnet ships as a single static Go binary and PostgreSQL comes from Alpine's package repository, so there is no Docker and no Compose file to maintain inside the guest.

## Why a VM, and why the kill-switch

bitmagnet works by crawling the BitTorrent DHT. That is not a passive activity: the crawler continuously announces itself to the swarm, so every peer it contacts learns the address it is crawling from. Running bitmagnet directly on a host publishes that host's IP address to tens of thousands of peers.

This template addresses that by putting bitmagnet in its own VM behind a **default-deny firewall**, not merely a route through a tunnel. The guarantee is: **if WireGuard is not up, no software in the guest can reach the internet.** Not just bitmagnet — anything, running as any user.

The guest's `OUTPUT` policy is `DROP`, and the exceptions carved out of it are deliberately narrow, because a default-deny policy is only as strong as its holes:

| Exception | Scope |
|-----------|-------|
| The tunnel | `-o wg0` — the only unrestricted egress, and only while wg0 is up |
| Loopback | `-o lo` |
| WireGuard handshake | UDP to **the endpoint's own resolved addresses**, not the whole internet on that port |
| SSH replies | `--sport 22` **and** conntrack `ESTABLISHED` — cannot open new connections |
| DHCP renewal | `--sport 68 --dport 67` to the broadcast address only |

The pinning matters. Two natural-looking rules quietly defeat the whole thing: a bare `--dport 51820 -j ACCEPT` lets any process reach any host on the internet that happens to listen on that port, and a bare `--sport 22 -j ACCEPT` lets any process bind source port 22 and open new outbound connections. Both are usable covert channels with the tunnel down, so both are pinned instead.

As a second layer, bitmagnet's own traffic is rejected outright on any interface other than `wg0`, matched by group ownership — so even if one of the holes above were widened by mistake, the crawler specifically still cannot leave the tunnel.

If `wg0` fails to come up on first boot, or drops months later, the crawler cannot fall back to the LAN interface — it simply stops talking.

Failing closed is the entire point. A VM that has quietly stopped indexing is a recoverable problem; a VM that has quietly started announcing your home address to the swarm is not.

## Create

```sh
vee create bitmagnet --template bitmagnet \
  --wg-conf ~/wireguard/nordlynx.conf \
  --pg-data-dir /mnt/tank/bitmagnet-pg
```

`vee create` prompts for the WireGuard config when `--wg-conf` is omitted. The database password is generated automatically — it is never typed by a human and the database never listens off-loopback, so prompting for one would only invite a weak, reused password.

### NordVPN users

NordVPN's own client ships as a snap, and Alpine has no snapd — so unlike the [torrent](../torrent/) template, this one offers WireGuard only. That is not a loss of coverage: **NordLynx is WireGuard**. Export a NordLynx configuration from your NordVPN account and pass it with `--wg-conf` and everything works exactly as it would with the native client, including the kill-switch.

### Running without a VPN

Declining the VPN does not merely warn — it **disables the DHT crawler**. Crawling announces the guest's address to every peer it contacts, and that damage is done long before anyone reads a warning in a log, so the template refuses to generate that traffic rather than trusting the operator to have meant it.

Everything else still runs: PostgreSQL, the web UI, the GraphQL API, and any torrents already in the index. The VM is a working indexer with an empty index. Re-create it with `--wg-conf` to start crawling.

If your host is already behind a tunnel at the network level, that is not something the guest can detect, so the crawler stays off regardless — pass a WireGuard config to turn it on.

## Defaults

| Setting | Value |
|---------|-------|
| Base image | Alpine Linux (latest), BIOS boot |
| Memory | 2G |
| CPUs | 2 |
| Disk | 8G |
| Network | User-mode NAT (pass `--nic-bridge` for a bridge) |
| Display | Headless |
| Web UI | TCP 3333, **not exposed** — reachable only over `vee tunnel` |
| DHT | UDP 3334, outbound through the tunnel only; crawler **off** unless a VPN is configured |
| Database | PostgreSQL 16, bound to `127.0.0.1` inside the guest |
| Config | `/var/lib/bitmagnet/config.yml` (mode `0600`; holds the database password) |

## The web UI

Port 3333 is bound inside the guest but the firewall drops it from every source, so there is no LAN-exposed, unauthenticated interface to forget about. Reach it over SSH instead:

```sh
vee tunnel bitmagnet
```

Then open the forwarded address that `vee tunnel` prints. This mirrors the torrent template: with a kill-switch up, SSH is the single management path into the guest, and everything else rides over it.

## Keeping the index on the host

Crawling the DHT into a useful index takes weeks to months, which makes the database the most valuable thing on the VM — and the thing you least want tied to the VM's lifetime.

`--pg-data-dir <host directory>` shares a host directory into the guest over virtiofs and uses it as PostgreSQL's data directory. The directory is created if it does not exist. With it set, you can delete and recreate the VM — to bump the bitmagnet version, or to change the VPN — and the crawled index is still there:

```sh
vee create bitmagnet --template bitmagnet \
  --wg-conf ~/wireguard/nordlynx.conf \
  --pg-data-dir /mnt/tank/bitmagnet-pg
```

Re-creating against a directory that already holds a cluster reuses it. `initdb` runs only when the directory has no `PG_VERSION` file, and the role and database creation are both guarded against already existing, so a second create against an existing index succeeds instead of erroring or overwriting.

Without `--pg-data-dir`, PostgreSQL stores its data on the VM's own qcow2 disk and the index is lost with the VM.

{{< hint info >}}
Point `--pg-data-dir` at a directory on real local storage. A network filesystem is not a safe place for a PostgreSQL data directory.
{{< /hint >}}

### Ownership on a virtiofs share

PostgreSQL insists on owning its data directory at mode `0700`, and virtiofs makes that awkward in a way worth knowing about if you ever debug this by hand.

virtiofs presents host ownership to the guest unchanged, and vee runs `virtiofsd` unprivileged — so the guest **cannot** chown the share. The intuitive fix (`chown postgres` on the mount) fails with `Operation not permitted`, and `initdb` then refuses the directory it cannot access, leaving a half-initialised cluster and a database that never starts.

The template does the reverse: it renumbers the guest's `postgres` account to the UID and GID that own the directory *on the host*, so postgres already owns the share and no chown is needed. Because that UID is usually 1000 — already taken by the Alpine image's own login user — the occupant is moved aside first. None of this needs any action from you; `vee create` reads the host directory's owner automatically.

## Under the hood

On first boot, cloud-init:

1. Waits for a real DHCP lease — Alpine reports networking as started when `dhcpcd` launches, not when it holds a lease.
2. Enables Alpine's `community` repository and installs PostgreSQL, `wireguard-tools`, `iptables`, and the guest agent.
3. Downloads the pinned bitmagnet release from GitHub and installs it to `/usr/local/bin/bitmagnet`, running it as a dedicated unprivileged `bitmagnet` user.
4. Mounts the virtiofs share (recording it in `/etc/fstab` so it survives a reboot), renumbers the guest's `postgres` user to the share's host owner, then initialises or reuses the PostgreSQL cluster and creates bitmagnet's role and database.
5. Starts bitmagnet under a hand-rolled OpenRC service — Alpine has no systemd — after waiting for PostgreSQL to accept connections, since bitmagnet applies its schema migrations on first start. The service is supervised and respawns: bitmagnet panics and exits if the database is not ready, so without supervision a transient startup race would leave a permanently dead crawler.
6. **Last of all**, installs the firewall rules and brings up `wg0`.

Step 6 is deliberately last. The steps above it need the network, and the kill-switch closes it; installing the deny policy earlier would leave the package and release downloads racing the tunnel.

The tunnel is also re-established at boot through `/etc/local.d/wg0.start`, because cloud-init `runcmd` steps fire only on first boot. Without it, a rebooted guest would come back with the kill-switch up and no tunnel — reachable over SSH and crawling nothing, which is the correct failure, but a confusing one.

## Checking on it

```sh
vee ssh bitmagnet -- 'wg show'                      # tunnel status
vee ssh bitmagnet -- 'rc-service bitmagnet status'  # crawler status
vee ssh bitmagnet -- 'tail -f /var/log/bitmagnet.log'
```

To see how much has been indexed so far:

```sh
vee ssh bitmagnet -- doas su postgres -c "psql -d bitmagnet -tAc 'select count(*) from torrents'"
```

To confirm the kill-switch is actually doing its job, bring the tunnel down and check that the guest goes silent rather than falling back to the LAN:

```sh
vee ssh bitmagnet -- 'wg-quick down wg0 && curl -m 5 https://example.com; echo "exit: $?"'
```

A non-zero exit is the correct result — it means traffic could not leave outside the tunnel. Bring it back up with `vee ssh bitmagnet -- 'wg-quick up wg0'`.

The covert channels are worth checking too, since those are the ones a bare default-deny policy misses:

```sh
# with wg0 down — every one of these should fail
vee ssh bitmagnet -- doas sh -c 'echo x | nc -u -w2 1.1.1.1 51820'   # not the endpoint
vee ssh bitmagnet -- doas su bitmagnet -s /bin/sh -c 'wget -qO- --timeout=5 http://1.1.1.1'
```
