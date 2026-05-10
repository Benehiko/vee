---
title: vee status
weight: 45
---

Show detailed status for a single VM, including live guest information via the QEMU guest agent.

```
vee status <name>
```

## Basic output (all VMs)

```
name      linux-gaming
template  passthrough
memory    16G
cpus      6
status    running
pid       12345
uptime    2h34m12s
spice     :5930
```

## Guest agent section (running VMs with guest agent)

If the VM is running and `guest_agent: true` is set in `vm.yaml`, vee connects to the QGA socket and queries the guest for live information:

```
guest-agent: connected

INTERFACE     IP              MAC
enp1s0        192.168.1.42/24  52:54:54:8d:72:76

hostname: linux-gaming
os:       Ubuntu 24.04.2 LTS
uptime:   up 2 hours, 34 minutes
```

## Requirements

Guest agent info requires `qemu-guest-agent` installed inside the VM:

```sh
sudo apt install qemu-guest-agent   # Debian/Ubuntu
sudo pacman -S qemu-guest-agent     # Arch
```

And `guest_agent: true` in `vm.yaml`.
