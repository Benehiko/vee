---
title: Networking
weight: 30
---

vee supports two networking modes: **user-mode NAT** and **bridge**.

## User-mode NAT (default)

The default mode for most templates. QEMU handles NAT transparently — no host configuration needed. The VM gets outbound internet access and vee forwards SSH via a random host port.

Limitations: the VM is not reachable from other hosts on the LAN, and inbound connections other than SSH require `vee tunnel`.

## Bridge networking

Bridge mode puts the VM directly on the LAN. The VM gets a real IP from your router's DHCP server and is reachable from any device on the network. Required for TrueNAS, gaming VMs, and any workload that needs to be a real LAN host.

### Create a persistent bridge (systemd-networkd)

Create `/etc/systemd/network/20-br0.netdev`:

```ini
[NetDev]
Name=br0
Kind=bridge
```

Create `/etc/systemd/network/21-br0-bind.network` (replace `enp6s0` with your physical interface):

```ini
[Match]
Name=enp6s0

[Network]
Bridge=br0
```

Create `/etc/systemd/network/22-br0.network`:

```ini
[Match]
Name=br0

[Network]
DHCP=yes
```

Enable and start:

```sh
sudo systemctl enable --now systemd-networkd
sudo networkctl reload
```

### Allow QEMU bridge access

QEMU needs permission to attach to the bridge without root:

```sh
echo "allow br0" | sudo tee /etc/qemu/bridge.conf
sudo chmod u+s /usr/lib/qemu/qemu-bridge-helper
```

### Configure a VM to use bridge mode

In `~/.vee/vms/<name>/vm.yaml`:

```yaml
nic:
  mode: bridge
  bridge: br0
  model: virtio-net-pci
  mac: 52:54:54:xx:xx:xx   # assign a stable MAC so DHCP gives a stable IP
```

Or pass `--nic-mode bridge` to `vee create`.

## Disk passthrough group

VMs that use raw block device passthrough (TrueNAS, custom NVMe) require your user to be in the `disk` group:

```sh
sudo usermod -aG disk $USER
```
