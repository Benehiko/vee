# ▸ vee

```
╔══════════════════════════════════════════════════════════════╗
║  VEE :: QEMU/KVM VM MANAGER                    v0.x.0       ║
║  jack in. spin up. jack out.                                 ║
╚══════════════════════════════════════════════════════════════╝
```

Bare-metal VM control from the terminal. QEMU/KVM backend, GPU passthrough, virtiofs sharing, SPICE display, SSH tunnelling — wired together into a single command.

---

## ▸ JACK IN — QUICK START

```sh
make install          # flash binary to ~/.vee/bin/vee
vee create myvm       # spin up an Ubuntu 24.04 server VM
vee start myvm        # boot — detached by default
vee ssh myvm          # open a shell
vee stop myvm         # graceful shutdown
```

> **Prerequisites:** See [docs/prerequisites.md](docs/prerequisites.md) — KVM access, bridge networking, disk group membership, OVMF firmware.

---

## ▸ TEMPLATES

```
╔═══════════════════╦══════════════════════════════════════════════════════╗
║ TEMPLATE          ║ DESCRIPTION                                          ║
╠═══════════════════╬══════════════════════════════════════════════════════╣
║ ubuntu-server     ║ Ubuntu 24.04 LTS · UEFI · user-mode NIC (default)   ║
║ devbox            ║ Docker + zsh via cloud-init · --distro flag          ║
║ server            ║ openssh + ufw + fail2ban · --distro flag             ║
║ truenas           ║ TrueNAS SCALE · AHCI OS disk · bridge NIC · SPICE   ║
║ gaming            ║ GPU passthrough · 16G RAM · 6 CPUs · anti-detect     ║
║ torrent           ║ Lightweight · qbittorrent-nox via cloud-init         ║
║ jellyfin          ║ Jellyfin · NFS/SMB/host-dir/block/USB media · mDNS   ║
║ windows           ║ Windows · UEFI secboot · TPM 2.0                     ║
║ docker            ║ Alpine Linux · Docker-over-TCP                       ║
╚═══════════════════╩══════════════════════════════════════════════════════╝
```

```sh
vee create mynas --template truenas \
  --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0S3H6:EXOS22TB-A \
  --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0WD9J:EXOS22TB-B
```

---

## ▸ GPU PASSTHROUGH

`gaming` and `passthrough` templates use VFIO to wire a PCIe GPU directly into the VM — zero emulation, full metal.

### HOST REQUIREMENTS

**1 · IOMMU — enable in kernel parameters**

```
# Intel
intel_iommu=on iommu=pt

# AMD
amd_iommu=on iommu=pt
```

**2 · vfio-pci kernel modules**

```sh
# /etc/modules-load.d/vfio.conf
vfio
vfio_iommu_type1
vfio_pci
```

**3 · vfio group membership**

```sh
sudo usermod -aG vfio $USER
```

**4 · Unlimited locked memory** — VFIO DMA-maps all guest RAM

```sh
sudo tee /etc/security/limits.d/vee-vfio.conf <<'EOF'
* - memlock unlimited
EOF
```

Re-login. Verify: `ulimit -l` → `unlimited`

### BIND THE GPU

```sh
# Scan the grid — list PCI addresses and IOMMU groups
vee gpu list

# Jack it in (requires root)
sudo vee gpu bind 08:00.0

# Pre-flight — verify all checks pass before boot
vee gpu status 08:00.0 --memory 16G
```

All devices in the same IOMMU group must be bound together. `vee gpu status` reports peer devices and their current drivers.

### CREATE A GAMING VM

```sh
# Passthrough VM booting from an existing NVMe (Windows or Linux)
vee create linux-gaming --template passthrough \
  --nvme-dev /dev/disk/by-id/nvme-... \
  --ovmf-vars /path/to/OVMF_VARS.fd \
  --gpu-pci 08:00.0

# Fresh Windows gaming VM
vee create win-gaming --template gaming --gpu-pci 08:00.0
```

### DEBUG PASSTHROUGH

```sh
# Pre-flight check
vee gpu status 08:00.0 --memory 16G

# QEMU log — scan for vfio errors post-boot
vee logs linux-gaming

# Structured debug log (VFIO decisions logged at start time)
tail -f ~/.float/state/logs/vee.log
```

```
╔═══════════════════════════════════════════╦══════════════════════════════════════════╦═════════════════════════════════════════╗
║ ERROR                                     ║ CAUSE                                    ║ FIX                                     ║
╠═══════════════════════════════════════════╬══════════════════════════════════════════╬═════════════════════════════════════════╣
║ Permission denied /dev/vfio/N             ║ User not in vfio group                   ║ sudo usermod -aG vfio $USER + re-login  ║
║ vfio_container_dma_map = -12 (ENOMEM)    ║ memlock limit too low                    ║ Set memlock unlimited in limits.d/      ║
║ QEMU process exited immediately           ║ Driver not bound / IOMMU isolation       ║ vee gpu status to diagnose              ║
║ GPU not used in guest                     ║ PCIAddr wrong in vm.yaml                 ║ Check gpu.pci_addr in vm.yaml           ║
╚═══════════════════════════════════════════╩══════════════════════════════════════════╩═════════════════════════════════════════╝
```

---

## ▸ COMMANDS

```
╔═══════════════════════════════════╦══════════════════════════════════════════════════════╗
║ COMMAND                           ║ DESCRIPTION                                          ║
╠═══════════════════════════════════╬══════════════════════════════════════════════════════╣
║ vee create <name>                 ║ Provision a new VM                                   ║
║ vee start <name>                  ║ Boot a VM (detached by default)                      ║
║ vee stop <name>                   ║ Graceful shutdown                                    ║
║ vee list                          ║ List all VMs and status                              ║
║ vee ssh <name>                    ║ Open a shell                                         ║
║ vee tunnel <name> <port>          ║ Forward a VM port to a random local port via SSH     ║
║ vee ports <name>                  ║ List bound TCP ports in a running VM (guest agent)   ║
║ vee logs <name>                   ║ Stream QEMU output                                   ║
║ vee monitor <name>                ║ Real-time CPU / memory / disk / network stats        ║
║ vee view <name>                   ║ Open VM display (SPICE or GPU)                       ║
║ vee delete <name>                 ║ Wipe VM and all its disks                            ║
║ vee gpu list                      ║ List PCI GPUs and IOMMU groups                       ║
║ vee gpu bind <pci>                ║ Bind device to vfio-pci                              ║
║ vee gpu unbind <pci>              ║ Release device back to host driver                   ║
║ vee gpu status <pci>              ║ Pre-flight check for passthrough                     ║
║ vee mirror start                  ║ Start host-side pacman caching proxy                 ║
║ vee mirror status                 ║ Show pacoloco unit state and cache size              ║
║ vee mirror stop                   ║ Stop the pacoloco user unit                          ║
║ vee mirror purge                  ║ Delete all cached packages on disk                   ║
║ vee version                       ║ Print version, commit, and build date                ║
╚═══════════════════════════════════╩══════════════════════════════════════════════════════╝
```

---

## ▸ SHELL COMPLETION

```sh
# bash
source <(vee completion bash)

# zsh
source <(vee completion zsh)

# fish
vee completion fish | source
```

---

## ▸ DOCS

- [docs/prerequisites.md](docs/prerequisites.md) — system setup, groups, bridge networking, OVMF
- [docs/gpu-passthrough-gaming.md](docs/gpu-passthrough-gaming.md) — Sunshine + Moonlight streaming over GPU passthrough
- [docs/pacman-mirror.md](docs/pacman-mirror.md) — host-side pacman caching proxy for Arch VMs

---

```
[ vee ] :: ALL SYSTEMS NOMINAL :: READY FOR BOOT
```
