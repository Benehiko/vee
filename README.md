# vee

A command-line VM manager built on QEMU/KVM. Create, start, SSH into, and monitor virtual machines from a single lightweight tool — with GPU passthrough, virtiofs sharing, SPICE display, and SSH tunnelling wired in.

```sh
vee create myvm    # create an Ubuntu 24.04 server VM
vee start myvm     # boot it (detached by default)
vee ssh myvm       # open a shell
vee stop myvm      # graceful shutdown
```

## Quick start

```sh
make install       # build and install to ~/.vee/bin/vee
vee create myvm    # create an Ubuntu 24.04 server VM
vee start myvm     # boot — detached by default
vee ssh myvm       # open a shell
vee stop myvm      # graceful shutdown
```

> **Prerequisites:** KVM access, bridge networking, disk group membership, and OVMF firmware. See [docs/prerequisites.md](docs/prerequisites.md).
>
> **macOS (Apple Silicon):** vee also runs on Apple Silicon Macs via Hypervisor.framework (HVF) with aarch64 guests and accelerated virtio-gpu. See [docs/macos.md](docs/macos.md) for setup, the per-guest GPU matrix, and limitations.
>
> **Windows:** vee also runs on Windows (amd64) via the Windows Hypervisor Platform (WHPX) with x86-64 guests. VFIO, virtiofs, vsock, bridge networking, and swtpm are Linux-only and degrade gracefully. See [docs/windows.md](docs/windows.md) for prerequisites and limitations.

## Templates

Templates apply sane defaults (memory, CPUs, disks, networking, cloud-init) automatically.

| Template | Description |
|----------|-------------|
| `ubuntu-server` | Ubuntu 24.04 LTS · UEFI · user-mode NIC (default) |
| `devbox` | Docker + zsh via cloud-init · `--distro` flag (ubuntu/arch/fedora) |
| `server` | openssh + ufw + fail2ban via cloud-init · `--distro` flag |
| `desktop` | GNOME + Mesa · accelerated virtio-gpu (virgl) · `--distro` flag (fedora/ubuntu) · Apple Silicon |
| `gaming-arch` | Arch Linux + KDE Plasma + Steam · 16G / 8 CPUs · virgl or GPU passthrough |
| `gaming-bazzite` | Bazzite (Fedora Atomic) gaming ISO · 16G / 8 CPUs · KDE Plasma |
| `gaming` | Legacy alias for `gaming-arch` with passthrough |
| `passthrough` | Raw NVMe boot + GPU passthrough · 16G / 6 CPUs · SPICE · virtiofs |
| `truenas` | TrueNAS SCALE · AHCI OS disk · bridge NIC · SPICE |
| `torrent` | Lightweight 4G / 2 CPUs · qbittorrent-nox via cloud-init |
| `jellyfin` | Jellyfin · NFS/SMB/host-dir/block/USB media · mDNS |
| `windows` | Windows · UEFI secure boot · TPM 2.0 |
| `docker` | Alpine Linux · Docker daemon on `tcp://localhost:2375` |
| `github-runner` | Self-hosted Actions runner · outbound HTTPS long-polling |

```sh
vee create mynas --template truenas \
  --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0S3H6:EXOS22TB-A \
  --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0WD9J:EXOS22TB-B
```

## Base images

`vee create` downloads the base image it needs automatically. You can also pre-fetch
images into the local cache (`~/.vee/iso/`) with `vee pull`, so later VMs reuse the
cached copy instead of re-downloading. A pull for an already-cached image is a no-op.

```sh
vee pull ubuntu            # newest known Ubuntu cloud image
vee pull ubuntu 22.04      # a specific version
vee pull ubuntu-24.04      # same, as a single token
vee pull windows win10     # build the Windows 10 ISO (see below)
vee pull --list            # list every supported distro and version
```

Both the distro and `distro-version` forms shell-complete from the built-in list.

| Distro | Notes |
|--------|-------|
| `ubuntu` | Cloud image (cloud-init ready) — 24.04, 22.04, 20.04 |
| `arch` | Bootstrap image |
| `fedora` | Cloud image (cloud-init ready) — 42, 41 · aarch64 + x86_64 |
| `alpine` | Cloud image |
| `bazzite` | Fedora Atomic gaming ISO |
| `truenas` | TrueNAS SCALE installer ISO |
| `windows` | Built on demand — `win11`, `win10`, `server2025`, `server2022` |

### Windows ISOs

vee builds Windows install ISOs on demand — no manual ISO download required. It
resolves the latest build via the [UUP dump](https://uupdump.net/) API, downloads
the ESD packages directly from Microsoft's servers, and assembles a bootable UEFI
ISO inside a throwaway container (`wimlib` + `xorriso`).

```sh
vee pull windows win11             # Windows 11 24H2
vee pull windows win10             # Windows 10 22H2
vee pull windows server2025        # Windows Server 2025
vee create winvm --template windows   # pulls automatically if not cached
```

> **Windows-guest status:** both `win10` (22H2) and `win11` (24H2) install
> end-to-end, booting to the desktop from the virtio system disk. 24H2 required
> several workarounds (running Setup from a writable scratch disk, injecting
> drivers via `offlineServicing`, and bundling `winre.wim` into `install.wim`).
> See [docs/windows-guests.md](docs/windows-guests.md) for the ISO pipeline and
> [docs/windows-24h2-install.md](docs/windows-24h2-install.md) for the full 24H2
> debugging writeup.

**Requirements:** `nerdctl` or `docker` on `PATH` (the ISO is assembled in a
container; no host tooling is installed) and ~15 GB of free scratch space, which
vee allocates next to the ISO cache (under `~/.vee/iso/`) so the build works even
when `/tmp` is a small RAM-backed `tmpfs`. The `windows` template additionally
pulls the VirtIO driver ISO and WinFSP so the guest gets paravirtualized disk,
network, and virtiofs support out of the box.

> **Licensing:** vee downloads Windows bits from Microsoft's own servers and
> assembles the ISO locally — it never redistributes Windows. You still need a
> valid Windows license key to activate the guest.

## GPU passthrough

The `gaming`, `gaming-arch`, and `passthrough` templates use VFIO to wire a PCIe GPU directly into the VM — zero emulation, full metal.

### Host requirements

**1 · IOMMU** — enable in kernel parameters:

```
intel_iommu=on iommu=pt   # Intel
amd_iommu=on iommu=pt     # AMD
```

**2 · vfio-pci kernel modules** — `/etc/modules-load.d/vfio.conf`:

```
vfio
vfio_iommu_type1
vfio_pci
```

**3 · vfio group membership:**

```sh
sudo usermod -aG vfio $USER
```

**4 · Unlimited locked memory** — VFIO DMA-maps all guest RAM:

```sh
sudo tee /etc/security/limits.d/vee-vfio.conf <<'EOF'
* - memlock unlimited
EOF
```

Re-login, then verify with `ulimit -l` → `unlimited`.

### Bind the GPU

```sh
vee gpu list              # list PCI addresses and IOMMU groups
sudo vee gpu bind 08:00.0 # bind to vfio-pci (requires root)
vee gpu status 08:00.0 --memory 16G  # pre-flight check before boot
```

All devices in the same IOMMU group must be bound together. `vee gpu status` reports peer devices and their current drivers.

### Create a gaming VM

```sh
# Passthrough VM booting from an existing NVMe (Windows or Linux)
vee create linux-gaming --template passthrough \
  --nvme-dev /dev/disk/by-id/nvme-... \
  --ovmf-vars /path/to/OVMF_VARS.fd \
  --gpu-pci 08:00.0

# Fresh Arch gaming VM with passthrough
vee create arch-gaming --template gaming-arch \
  --gpu-mode passthrough --gpu-pci 08:00.0
```

### Debug passthrough

```sh
vee gpu status 08:00.0 --memory 16G   # pre-flight check
vee logs linux-gaming                 # QEMU log — scan for vfio errors
tail -f ~/.vee/logs/vee.log           # structured debug log (VFIO decisions)
```

| Error | Cause | Fix |
|-------|-------|-----|
| `Permission denied /dev/vfio/N` | User not in vfio group | `sudo usermod -aG vfio $USER` + re-login |
| `vfio_container_dma_map = -12 (ENOMEM)` | memlock limit too low | Set `memlock unlimited` in `limits.d/` |
| QEMU process exits immediately | Driver not bound / IOMMU isolation | `vee gpu status` to diagnose |
| GPU not used in guest | Wrong `pci_addr` in `vm.yaml` | Check `gpu.pci_addr` in `vm.yaml` |

See [docs/gpu-passthrough-gaming.md](docs/gpu-passthrough-gaming.md) for Sunshine + Moonlight streaming.

## Commands

| Command | Description |
|---------|-------------|
| `vee create <name>` | Provision a new VM |
| `vee pull <distro> [version]` | Download or build a base image into the cache |
| `vee start <name>` | Boot a VM (detached by default) |
| `vee stop <name>` | Graceful shutdown |
| `vee list` | List all VMs and status |
| `vee status <name>` | Show detailed status of a VM |
| `vee ssh <name>` | Open a shell |
| `vee ssh-share <name>` | Share host SSH agent into the VM via AF_VSOCK |
| `vee tunnel <name> [service]` | List VM services, or open/connect to one |
| `vee ports <name>` | List bound TCP ports and process names in a running VM |
| `vee ip <name>` | Show network interfaces and IP addresses inside a VM |
| `vee logs <name>` | Stream QEMU output |
| `vee monitor <name>` | Real-time CPU / memory / disk / network stats |
| `vee qmp <name> <command>` | Send a QMP (QEMU Machine Protocol) command to a running VM |
| `vee view <name>` | Open or connect to a VM's display (SPICE or GPU) |
| `vee config <name>` | Edit a VM's configuration in an interactive TUI |
| `vee check <name>` | Run health checks on an installed VM |
| `vee backup <name>` | Back up directories from a running VM |
| `vee autostart <name>` | Enable or disable autostart for a VM |
| `vee delete <name>` | Wipe VM and all its disks |
| `vee daemon` | Run the vee daemon (starts and watches autostart VMs) |
| `vee dashboard` | Start a web dashboard for all VMs |
| `vee gpu list` | List PCI GPUs and IOMMU groups |
| `vee gpu bind <pci>` | Bind device to vfio-pci (requires root) |
| `vee gpu unbind <pci>` | Release device back to host driver (requires root) |
| `vee gpu status <pci>` | Pre-flight check for passthrough |
| `vee mirror start` | Start host-side pacman caching proxy (pacoloco) |
| `vee mirror status` | Show pacoloco unit state and cache size |
| `vee mirror stop` | Stop the pacoloco user unit |
| `vee mirror purge` | Delete all cached packages on disk |
| `vee runner key <name>` | Print a runner's GitHub SSH public key |
| `vee runner snapshot <name>` | Persist a runner's credentials to the host (encrypted) |
| `vee version` | Print version, commit, and build date |

## Shell completion

```sh
source <(vee completion bash)   # bash
source <(vee completion zsh)    # zsh
vee completion fish | source    # fish
```

## Development

```sh
make hooks   # enable the pre-commit hook (fmt check + lint + build) for this clone
make fmt     # apply gofumpt + goimports formatting in place
make lint    # format check + golangci-lint (mirrors CI)
make build   # build the vee binary
make test    # go test -race ./...
```

Formatting (`gofumpt` + `goimports`) and linting are enforced by a strict
`.golangci.yml`. `make lint` runs `golangci-lint fmt --diff` (fails on any
unformatted code) followed by `golangci-lint run`; run `make fmt` to fix
formatting before committing.

The pre-commit hook lives in `.githooks/` (tracked) and only runs when Go files
are staged: it runs the format check, lint, and build. Enable it once per clone
with `make hooks`; bypass a single commit with `git commit --no-verify`. CI
(lint + format + build + test) reads the Go version from `go.mod`.

## Releases

Pushing a `v*` tag triggers the release workflow
([.github/workflows/release.yml](.github/workflows/release.yml)):

```sh
git tag v0.4.0
git push origin v0.4.0
```

It cross-compiles the `vee` binary for every supported host — `linux/amd64`,
`linux/arm64`, `darwin/amd64`, `darwin/arm64` — with the tag, commit, and build
date injected via `-ldflags` (so `vee version` reports the release identity). Each
build is packaged as a `.tar.gz` (binary + `LICENSE` + `README.md` +
`THIRD_PARTY_LICENSES`) alongside a `.sha256` checksum, and a GitHub Release is
published whose body lists the commits since the previous tag. Tags containing a
hyphen (e.g. `v0.4.0-rc1`) are marked as pre-releases.

Windows binaries are not produced yet: vee is a host-side hypervisor driver that
depends on vsock, VFIO, and POSIX syscalls, so it currently only builds on Linux
and macOS.

## Docs

- [docs/prerequisites.md](docs/prerequisites.md) — system setup, groups, bridge networking, OVMF
- [docs/gpu-passthrough-gaming.md](docs/gpu-passthrough-gaming.md) — Sunshine + Moonlight streaming over GPU passthrough
- [docs/media-sources.md](docs/media-sources.md) — attaching NFS/SMB/host-dir/block/USB media to VMs
- [docs/pacman-mirror.md](docs/pacman-mirror.md) — host-side pacman caching proxy for Arch VMs
- [docs/host-shutdown.md](docs/host-shutdown.md) — how the daemon blocks host poweroff while VMs are running
- [docs/github-runner.md](docs/github-runner.md) — self-hosted GitHub Actions runner: cred persistence, SSH keys, disk GC

## License

MIT — see [LICENSE](LICENSE).
