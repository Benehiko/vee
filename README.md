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

## GPU passthrough

The `gaming` and `passthrough` templates use VFIO to pass a PCIe GPU directly into the VM.

### Host requirements

1. **IOMMU enabled** — add to kernel parameters:
   - Intel: `intel_iommu=on iommu=pt`
   - AMD: `amd_iommu=on iommu=pt`

2. **vfio-pci loaded** — usually happens automatically when binding; or add to `/etc/modules-load.d/vfio.conf`:
   ```
   vfio
   vfio_iommu_type1
   vfio_pci
   ```

3. **User in `vfio` group**:
   ```sh
   sudo usermod -aG vfio $USER
   ```

4. **Unlimited locked memory** — VFIO DMA-maps all guest RAM:
   ```sh
   sudo tee /etc/security/limits.d/vee-vfio.conf <<'EOF'
   * - memlock unlimited
   EOF
   ```
   Log out and back in. Verify with `ulimit -l` (should print `unlimited`).

### Binding the GPU to vfio-pci

```sh
# Find your GPU's PCI address and IOMMU group
vee gpu list

# Bind to vfio-pci (requires root)
sudo vee gpu bind 08:00.0

# Verify all checks pass before starting the VM
vee gpu status 08:00.0 --memory 16G
```

All devices in the same IOMMU group must also be bound to vfio-pci. `vee gpu status` reports peer devices and their drivers.

### Creating a gaming VM

```sh
# GPU passthrough VM booting from an existing NVMe (Windows or Linux)
vee create linux-gaming --template passthrough \
  --nvme-dev /dev/disk/by-id/nvme-... \
  --ovmf-vars /path/to/OVMF_VARS.fd \
  --gpu-pci 08:00.0

# Or a fresh Windows gaming VM with cloud-init disk
vee create win-gaming --template gaming --gpu-pci 08:00.0
```

### Debugging passthrough issues

```sh
# Pre-flight check (driver, IOMMU group, /dev/vfio/N, memlock)
vee gpu status 08:00.0 --memory 16G

# QEMU log — check for vfio errors after start
vee logs linux-gaming

# Structured debug log (all VFIO decisions logged at start time)
tail -f ~/.float/state/logs/vee.log
```

Common failure modes:

| Error | Cause | Fix |
|-------|-------|-----|
| `Permission denied /dev/vfio/N` | User not in `vfio` group | `sudo usermod -aG vfio $USER` + re-login |
| `vfio_container_dma_map = -12 (ENOMEM)` | `memlock` limit too low | Set `memlock unlimited` in `/etc/security/limits.d/` |
| `QEMU process exited immediately` | Driver not bound or IOMMU group isolation violated | Run `vee gpu status` to diagnose |
| GPU not used in guest | PCIAddr empty or wrong in `vm.yaml` | Check `~/.config/vee/vms/<name>/vm.yaml` `gpu.pci_addr` |

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
