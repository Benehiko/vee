---
title: vee network
weight: 125
---

Show a running VM's network state from both sides of the VM boundary: what the host can see of the guest, and what the guest reports about its own firewall, VPN, DNS, routes, and egress.

```
vee network <name> [--json] [--skip-egress]
```

VM networking is otherwise opaque — an IP and a MAC say nothing about whether the guest firewall is configured, whether the VPN tunnel is actually up, or whether all outbound traffic (including DNS) really leaves through it. `vee network` answers those questions in one report.

## What it reports

**Host section** — the host's view of the VM:

- NIC mode (`user` or `bridge`) and MAC address
- ARP/neighbour-table visibility and the resolved IP
- SSH port reachability
- Guest-agent availability

**Guest section** — probed inside the guest via the QEMU guest agent, falling back to SSH:

- Interfaces and addresses
- Default route (and whether it leaves through the VPN tunnel device)
- DNS servers
- `ufw` state (active, default outgoing policy, rule count)
- VPN state — `nordvpn status`/`settings` or `wg show`, depending on how the VM was created
- Egress IP and DNS egress IP

## Template-aware checks

For VPN-configured VMs (the [torrent template]({{< relref "/templates" >}})), the report includes pass/fail checks:

- **Kill-switch enabled** — NordVPN's daemon-enforced kill switch, or ufw `deny (outgoing)` for generic WireGuard
- **Default route through the tunnel** (`nordlynx` or `wg0`)
- **No DNS leak** — no DNS server pointing at the LAN resolver
- **Egress IP differs from the host's public IP** — traffic is actually tunnelled
- **DNS egress differs from the host's public IP** — DNS queries don't bypass the VPN

A kill-switched guest intentionally stops answering ARP on the LAN, so a missing ARP entry on a VPN VM is reported as `INFO` (expected), not a failure.

VMs without a VPN get the facts without pass/fail judgments.

## Egress lookups

The egress checks make one HTTPS request to Cloudflare's trace endpoint (`https://www.cloudflare.com/cdn-cgi/trace`) from the guest and one from the host, plus one DNS query (`whoami.cloudflare` via `1.1.1.1`) from the guest. Use `--skip-egress` to keep the report fully offline.

## Example

```sh
vee network torrent
```

```
host:
  nic          bridge (52:54:00:ab:cd:ef)
  ssh          127.0.0.1:2222
  qga          available
  public ip    82.1.2.3
guest:
  interfaces   enp1s0 192.168.1.50/24, nordlynx 10.5.0.2/32
  route        default dev nordlynx
  dns          103.86.96.100, 103.86.99.100
  ufw          active, outgoing=allow (3 rules)
  vpn          nordvpn connected (de1234.nordvpn.com), killswitch=on
  egress       185.130.184.90
  dns egress   185.130.184.91

checks: 8 passed, 0 failed, 1 info, 0 unavailable

CHECK             STATUS  DETAIL
host:arp          INFO    no ARP entry — expected: VPN kill-switch blocks LAN visibility
host:ssh          PASS    127.0.0.1:2222 reachable
host:qga          PASS    guest agent responding
guest:route       PASS    default dev nordlynx
guest:dns-leak    PASS    103.86.96.100, 103.86.99.100
guest:ufw         PASS    active, outgoing=allow (3 rules)
guest:vpn         PASS    nordvpn connected de1234.nordvpn.com
guest:killswitch  PASS    nordvpn kill switch enabled
guest:egress-ip   PASS    185.130.184.90 differs from host 82.1.2.3
guest:dns-egress  PASS    185.130.184.91 differs from host 82.1.2.3
```

Probes whose tooling is missing in the guest (no `dig`, no `ufw`) degrade to `UNAVAILABLE` rather than failing the report.

`--json` prints the full structured report; the same data is available over MCP as the `vm_network` tool.
