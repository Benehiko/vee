# vee

A command-line VM manager built on QEMU/KVM. Create, start, stop, SSH into, and tunnel ports for virtual machines from a single tool.

## Quick start

```sh
make install          # builds and installs to ~/.vee/bin/vee
vee create myvm       # create an Ubuntu 24.04 server VM
vee start myvm        # boot it (detached)
vee ssh myvm          # SSH in
vee stop myvm         # graceful shutdown
```

## Prerequisites

See [docs/prerequisites.md](docs/prerequisites.md) for required packages and system configuration (KVM access, bridge networking, disk group membership, OVMF).

## Templates

| Template       | Description |
|----------------|-------------|
| `ubuntu-server`| Ubuntu 24.04 LTS, UEFI, user-mode NIC (default) |
| `devbox`       | Docker + zsh via cloud-init; supports `--distro` |
| `server`       | openssh + ufw + fail2ban; supports `--distro` |
| `truenas`      | TrueNAS SCALE, AHCI OS disk, bridge NIC, SPICE display |
| `gaming`       | GPU passthrough, 16G RAM, 6 CPUs, anti-detect |
| `torrent`      | Lightweight, qbittorrent-nox via cloud-init |
| `windows`      | Windows, UEFI secboot, TPM 2.0 |

```sh
vee create mynas --template truenas \
  --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0S3H6:EXOS22TB-A \
  --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0WD9J:EXOS22TB-B
```

## Commands

| Command | Description |
|---------|-------------|
| `vee create <name>` | Create a new VM |
| `vee start <name>` | Start a VM |
| `vee stop <name>` | Stop a running VM |
| `vee list` | List all VMs and their status |
| `vee ssh <name>` | Open an SSH session |
| `vee tunnel <name> <port>` | Forward a VM port to a random local port via SSH |
| `vee ports <name>` | List bound TCP ports inside a running VM (requires guest agent) |
| `vee logs <name>` | Stream QEMU output |
| `vee monitor <name>` | Real-time CPU/memory/disk/network stats |
| `vee view <name>` | Open the VM display (SPICE or GPU) |
| `vee delete <name>` | Delete a VM and its disks |

## Shell completion

```sh
# bash
source <(vee completion bash)

# zsh
source <(vee completion zsh)

# fish
vee completion fish | source
```
