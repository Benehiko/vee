---
title: Background Tunnels
weight: 20
---

`vee tunnel` connects the host to a service running inside a VM. By default the
tunnel is a foreground process bound to `localhost`: it lives as long as the
command does, and only the host machine can reach it.

Background tunnels change both of those properties. They are handed to the vee
daemon, which keeps them up for as long as the VM is running and re-establishes
them automatically after a host reboot, and they can optionally be published to
the LAN so other devices reach them too.

## Quick reference

```sh
# Foreground tunnel on localhost (the default).
vee tunnel media jellyfin

# Foreground, but reachable from other devices on the LAN.
vee tunnel media jellyfin --host

# Persistent: the daemon keeps this tunnel up and restores it on boot.
vee tunnel media jellyfin --background

# Persistent and published to the LAN under its own name.
vee tunnel media jellyfin --host --background

# Show every background tunnel and whether it is currently served.
vee tunnel --list

# Remove a background tunnel.
vee tunnel media jellyfin --stop
```

## Binding to the LAN with `--host`

Without `--host`, every tunnel binds `127.0.0.1` and is reachable only from the
host. With `--host` it binds `0.0.0.0`, so any device that can route to the
host can reach the service.

**vee adds no authentication of its own.** Whatever login the guest application
enforces is the only thing protecting the service once it is on the LAN. That
is fine for an application with a real account system, and a genuine risk for
one that ships with default credentials or none at all. `vee tunnel --host`
therefore prints a warning and asks for confirmation before binding; pass
`--yes` to skip the prompt in scripts.

Two considerations worth weighing before publishing a service:

- **Kill-switched guests.** The `torrent` and `bitmagnet` templates deliberately
  drop LAN access to their service ports, so that traffic cannot escape the VPN
  tunnel. Publishing such a service with `--host` re-exposes on the host what
  the guest was configured to block.
- **The LAN is not a trust boundary.** Anything on the network — including
  devices you do not administer — can reach a published service.

## Persisting a tunnel with `--background`

`--background` records the tunnel in `~/.vee/tunnels.json` and hands it to the
daemon rather than holding it in the foreground. The command returns
immediately; the tunnel keeps running.

The daemon reconciles this registry on every poll tick (every 5 seconds):

- A registered tunnel whose VM is running gets a listener.
- A registered tunnel whose VM is stopped does not — a proxy accepting
  connections it could never fulfil is worse than a refused connection. The
  registration survives, and `vee tunnel --list` shows it as inactive with the
  reason.
- When the VM comes back, whether from `vee start` or the daemon's own
  autostart pass after a host reboot, the tunnel is re-established.

The guest's address is resolved once per incoming connection rather than once
when the tunnel starts, so a DHCP renewal that moves the guest is picked up
without restarting anything. Existing connections are unaffected — a browser
holding a keep-alive connection stays pinned to the old address until it opens
a new one.

The host port is recorded on first allocation and reused thereafter, so a
bookmark or DNS record pointing at a background tunnel stays valid across
reboots. Ports are allocated from the 2300–2399 range, above the range the
daemon's SSH loopback proxies use.

Background tunnels require the daemon. Install it with `vee daemon install`; see
[vee daemon](../../commands/daemon/) for what else the daemon does. Without a
daemon, `vee tunnel --list` still reports what is registered and says that
nothing is serving it.

SPICE services cannot be backgrounded — QEMU already binds the SPICE port on
the host, so there is no tunnel for the daemon to own.

## Name-based routing

An HTTP or HTTPS tunnel published with `--host` is also served by the daemon's
router on port 80, under its own hostname:

```
http://jellyfin.benehiko-desktop/
http://qbittorrent.benehiko-desktop/
```

The label is the service name by default; override it with `--hostname`. The
suffix is the host machine's own short hostname.

Routing by hostname rather than by path prefix is deliberate. Web applications
emit absolute asset URLs (`/static/...`, `/api/...`), which a stripped path
prefix breaks unless every application is separately configured with a base
URL — something Jellyfin supports and qBittorrent does not. Under a virtual
host, the application keeps the root path and needs no configuration at all.
The router also preserves the client's `Host` header, so links the application
generates point back at the published name.

Only HTTP and HTTPS tunnels are routed. A SPICE or raw TCP tunnel has no
meaningful HTTP representation and remains reachable on its own port.

Visiting the host's bare name, or any label that does not match a tunnel,
returns an index of the published names.

### Making the names resolve

Reaching a published name takes two steps, and vee only performs the second:

| Step | Question | Handled by |
| --- | --- | --- |
| Resolution | What address is `jellyfin.benehiko-desktop`? | your DNS — see below |
| Routing | Which tunnel serves this request? | the vee router |

The router matches on the `Host` header of a request that has already arrived.
It publishes no DNS records, and it does not care what resolved the name — a
wildcard record, a single A record, and a line in `/etc/hosts` all reach the
same router. Until something resolves the name, though, the client fails before
a request is ever sent, and the router never sees it.

This split is deliberate. Name resolution is a property of your network, not of
the VM manager, so vee leaves it to whatever already runs DNS for you.

Any of these work:

- **A wildcard record**, `*.benehiko-desktop` pointing at the host's LAN
  address. The least ongoing work: new tunnels resolve without further DNS
  changes.
- **One record per name**, each pointing at the host's address. More explicit,
  and keeps unpublished names unresolvable.
- **`/etc/hosts` on each client.** No DNS server needed. Fine for one or two
  devices, tedious beyond that.

Where you add a record depends on what serves DNS on your network — a router,
a Pi-hole, an AdGuard Home instance, or a VM running the `dns-sink` template.
On AdGuard Home (which `dns-sink` runs), a wildcard goes under
**Filters → DNS rewrites** as `*.benehiko-desktop` answering the host's LAN
address. That is runtime state in the VM, so recreating it from the template
does not carry the rewrite over.

mDNS (`.local`) does not cover this case on its own: Avahi publishes the host's
own name, not arbitrary subdomains of it, so a DNS entry of some kind is
required either way.

### Port 80 and capabilities

The router binds port 80, which is privileged. The systemd unit written by
`vee daemon install` grants `CAP_NET_BIND_SERVICE` so the daemon can bind it
while still running as your user account.

If the port cannot be bound — because the daemon was started outside the unit,
or something else already holds port 80 — the daemon logs the reason once and
carries on. Every tunnel is still reachable directly on its own port; only the
name-based URLs are unavailable.

## Reading `vee tunnel --list`

```
VM        SERVICE      BIND       PORT  STATE     URL
media     jellyfin     0.0.0.0    2301  active    http://jellyfin.benehiko-desktop/
torrents  qbittorrent  127.0.0.1  2302  active    http://localhost:2302
```

- **BIND** — `127.0.0.1` for host-only, `0.0.0.0` for LAN-published.
- **STATE** — `active` when the daemon holds a listener, `inactive` with a
  reason otherwise (VM not running, service no longer declared, daemon not
  running).
- **URL** — the published name for a routed HTTP tunnel, otherwise `host:port`.

## Where the state lives

`~/.vee/tunnels.json` holds the registry. It is written atomically, so the
daemon never reads a half-written file, and is owner-readable only. Deleting a
VM removes its tunnels from the registry.
