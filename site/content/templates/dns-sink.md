---
title: dns-sink
weight: 76
---

A minimal Alpine Linux VM running [AdGuard Home](https://adguard.com/adguard-home/overview.html) as a network-wide DNS sinkhole. Queries for known advertising, tracking, and malware domains are answered with a null response instead of being forwarded, so every device that uses the VM as its resolver is covered — including devices that cannot run a content blocker themselves, such as smart TVs, consoles, and phones.

The VM is deliberately small: 512 MB of RAM, one vCPU, and a 4 GB disk are enough for a household-sized resolver. AdGuard Home ships as a single static Go binary, so nothing beyond the Alpine base system is installed.

## Create

```sh
vee create dns --template dns-sink --nic-mode bridge
```

`vee create` prompts for the AdGuard Home admin password (entered twice, echo suppressed) before the VM is built. Only a bcrypt hash of the password is written into the cloud-init seed — the plaintext is never stored on the host or inside the guest.

Choose a different admin username with `--dns-admin-user`:

```sh
vee create dns --template dns-sink --nic-mode bridge --dns-admin-user alano
```

Bridge networking is required. The template refuses `--nic-mode=user` because QEMU's user-mode NAT gives the VM no LAN-routable address, so no other host on the network could reach the resolver.

## Defaults

| Setting | Value |
|---------|-------|
| Base image | Alpine Linux (latest), BIOS boot |
| Memory | 512M |
| CPUs | 1 |
| Disk | 4G |
| Network | Bridge (`br0`) |
| Display | Headless |
| DNS | UDP + TCP 53, open to the LAN |
| Admin UI | TCP 3000, restricted to RFC1918 sources |
| Hostname | VM name, published via `avahi-daemon` |

## Pointing clients at the sinkhole

First find the VM's bridge address:

```sh
vee ip dns        # reports the VM's bridge address once the guest agent is up
```

Then either:

- **Whole network (recommended).** Set the DNS server on your router's DHCP configuration to the VM's IP. Every device that renews its lease picks it up automatically. Give the VM a DHCP reservation first so its address does not change.
- **Per device.** Set the DNS server manually in the device's network settings.

Because the address must stay stable, a DHCP reservation on the router — keyed to the VM's MAC address — is the most reliable setup.

## Admin UI

The AdGuard Home dashboard shows query logs, per-client statistics, and blocklist management:

```
http://<vm-name>.local:3000
```

`avahi-daemon` publishes the VM's hostname over multicast DNS, so `.local` resolution works from most Linux, macOS, and recent Windows clients without any DNS configuration. If mDNS is unavailable, use the VM's IP directly.

From the host, the UI can also be reached through a tunnel without touching the LAN:

```sh
vee tunnel dns 3000
vee ssh dns
```

If you accept an empty admin password at create time, the UI has no login at all. The guest firewall still restricts port 3000 to RFC1918 source addresses, so it is not reachable from the internet, but any device on your LAN can change the resolver's configuration. Setting a password is strongly preferred.

## Upstream resolvers and privacy

Queries that are not blocked are forwarded over **DNS-over-TLS** to Cloudflare (`1.1.1.1`) and Quad9 (`9.9.9.9`) in load-balancing mode, so traffic leaving the VM is encrypted and not visible to the local network or the ISP. Plain DNS is used only to bootstrap — that is, to resolve the upstream servers' own hostnames. DNSSEC validation is enabled.

Change the upstreams in the admin UI under **Settings → DNS settings**; edits made there persist in `/opt/AdGuardHome/AdGuardHome.yaml` inside the guest and survive reboots.

## Blocklists

Five lists are enabled on first boot and refreshed every 24 hours:

| List | Focus |
|------|-------|
| AdGuard DNS filter | General ads and trackers |
| AdAway Default Blocklist | Mobile advertising |
| StevenBlack unified hosts | Ads, trackers, and malware |
| The Big List of Hacked Malware Web Sites | Compromised sites serving malware |
| Malicious URL Blocklist (URLHaus) | Active malware distribution URLs |

AdGuard Home's built-in Safe Browsing check is also on, which blocks domains flagged as phishing or malware even when they are not on any list.

Add, remove, or disable lists in the admin UI under **Filters → DNS blocklists**. Per-domain exceptions go under **Filters → Custom filtering rules** — useful when a blocklist breaks a site you need, since an over-broad list is the usual cause of "this page suddenly stopped working" after deploying a sinkhole.

## Firewall

The guest runs `iptables` with a default-drop INPUT policy. Only these are accepted:

- SSH (22/tcp) — for `vee ssh`
- DNS (53/udp and 53/tcp) — from anywhere on the LAN
- mDNS (5353/udp) — for hostname publishing
- Admin UI (3000/tcp) — from RFC1918 sources only
- ICMP, and established or related connections

Rules are saved with `/etc/init.d/iptables save` and restored on boot.

## Service supervision

Alpine uses OpenRC rather than systemd, so the template installs a hand-written init script at `/etc/init.d/adguardhome` that runs the binary under `start-stop-daemon`. Manage it the usual OpenRC way.

The guest login is the Alpine cloud image's own `alpine` user rather than the `vee` user other templates create — cloud-init assigns new users `/bin/bash`, which the Alpine image does not ship, so creating one would fail and take the SSH key setup down with it. Privilege escalation uses `doas`; the Alpine base has no `sudo`.

```sh
vee ssh dns
doas rc-service adguardhome status
doas rc-service adguardhome restart
doas tail -f /var/log/adguardhome.log
```

## Upgrading AdGuard Home

The installed version is pinned in the template so VM creation stays reproducible. To upgrade an existing VM in place:

```sh
vee ssh dns
doas rc-service adguardhome stop
curl -fsSL -o /tmp/agh.tar.gz \
  https://github.com/AdguardTeam/AdGuardHome/releases/download/<version>/AdGuardHome_linux_amd64.tar.gz
tar -xzf /tmp/agh.tar.gz -C /tmp
doas install -m 0755 /tmp/AdGuardHome/AdGuardHome /usr/local/bin/AdGuardHome
doas rc-service adguardhome start
```

The in-app updater is disabled (`--no-check-update`) so that the binary under `/usr/local/bin` is never replaced behind vee's back.

## Availability

Every device pointed at the sinkhole depends on it for name resolution, so the VM going down takes DNS with it. Two mitigations are worth setting up:

- Configure a **secondary DNS server** on the router (for example `1.1.1.1`) so clients fall back when the VM is unreachable. Note that clients may use the secondary opportunistically, which lets some queries bypass filtering.
- Start the VM automatically with the host. See [`vee autostart`]({{< relref "/commands/autostart" >}}) for enabling the vee daemon's VM autostart.
