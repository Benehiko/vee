# vee

A command-line VM manager built on QEMU/KVM, and on Apple's Virtualization.framework for macOS guests. Create, start, SSH into, and monitor virtual machines from a single lightweight tool — with GPU passthrough, virtiofs sharing, SPICE display, and SSH tunnelling wired in.

The backend is a per-VM choice, independent of the guest OS. Every guest runs on QEMU by default, with two exceptions on Apple Silicon hosts: macOS guests always run on Virtualization.framework (QEMU's experimental macOS machine does not work on current macOS hosts), and Linux guests can opt into it with `--backend vz` — which is the only way to give a Linux guest a host↔guest vsock device on a macOS host (see [docs/linux-vz.md](docs/linux-vz.md)).

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
> **macOS as a host (Apple Silicon):** vee runs on Apple Silicon Macs, driving QEMU through Hypervisor.framework (HVF) with aarch64 guests. GPU acceleration is the weak spot — the published QEMU bundle is a plain-HVF build, because the pinned QEMU and the only macOS-patched virglrenderer cannot currently be compiled together. See [docs/macos.md](docs/macos.md) for setup, the per-guest GPU matrix, and limitations.
>
> **macOS as a guest (Apple Silicon):** vee can also restore and run a macOS VM — `vee create mymac --template macos` — on Apple's Virtualization.framework. This is a different thing from the note above, and it needs the `vee-vz-helper` binary, which ships beside `vee` in the `darwin-arm64` release tarball; on an Apple Silicon checkout, `make install` builds and installs it alongside `vee`. Apple's licence permits macOS VMs only on Apple hardware and at most two at a time. See [macOS guests](https://vee.benehiko.com/getting-started/macos-guests/) or [docs/macos.md](docs/macos.md#macos-guests-virtualizationframework).
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
| `passthrough` | Raw NVMe boot + GPU passthrough · 8G / 6 CPUs · SPICE · virtiofs |
| `truenas` | TrueNAS SCALE · 6G / 2 CPUs · AHCI OS disk · per-drive iothreads · bridge NIC · SPICE |
| `torrent` | Lightweight 2G / 1 CPU · qbittorrent-nox via cloud-init · optional VPN kill-switch |
| `jellyfin` | Jellyfin · NFS/SMB/host-dir/block/USB media · mDNS |
| `dns-sink` | Alpine Linux · AdGuard Home DNS sinkhole · 512M / 1 CPU · LAN-wide ad and malware blocking · bridge NIC |
| `windows` | Windows · UEFI · secure boot + TPM 2.0 on x86_64 · arm64 (Apple Silicon) boots NVMe + ramfb with the hardware checks bypassed |
| `docker` | Alpine Linux · Docker daemon on `tcp://localhost:2375` |
| `github-runner` | Self-hosted Actions runner · outbound HTTPS long-polling |
| `macos` | macOS guest on Apple's Virtualization.framework · 8G / 4 CPUs · Apple Silicon hosts only · restores from an IPSW or imports a [macosvm](https://github.com/s-u/macosvm) bundle |

```sh
vee create mynas --template truenas \
  --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0S3H6:EXOS22TB-A \
  --data-disk /dev/disk/by-id/ata-ST22000NM000C_ZXA0WD9J:EXOS22TB-B
```

To place a single VM's managed boot disk on another disk (e.g. a fast NVMe) while
the rest stay on the default, pass a directory to `--boot-disk-path`
(`vee create win --template windows --boot-disk-path /mnt/nvme`) — see
[docs/configuration.md](docs/configuration.md#per-vm-boot-disk-location).

## Base images

`vee create` downloads the base image it needs automatically. You can also pre-fetch
images into the local cache (`~/.vee/iso/`) with `vee pull`, so later VMs reuse the
cached copy instead of re-downloading. A pull for an already-cached image is a no-op.

Both the image cache and the VM storage directory can be relocated to another disk
via `~/.vee/config.yaml` (`iso_cache_path` / `storage_path`) — see
[docs/configuration.md](docs/configuration.md).

```sh
vee pull ubuntu            # newest known Ubuntu cloud image
vee pull ubuntu 22.04      # a specific version
vee pull ubuntu-24.04      # same, as a single token
vee pull windows win10     # build the Windows 10 ISO (see below)
vee pull macos             # newest macOS restore image this host can install
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
| `windows` | Built on demand, matching the host arch — `win11`, `win10`, `server2025`, `server2022` on x86_64; `win11`, `win10` on arm64 (UUP dump publishes no arm64 Server feature builds) |
| `macos` | Restore image (IPSW, 15-20 GB) — `latest` asks the host which image it can install; an `https://…ipsw` URL from [ipsw.me](https://ipsw.me/product/Mac/) pins an older one · Apple Silicon macOS hosts only |

### Windows ISOs

vee builds Windows install ISOs on demand — no manual ISO download required. It
resolves the latest build via the [UUP dump](https://uupdump.net/) API, downloads
the ESD packages directly from Microsoft's servers, and assembles a bootable UEFI
ISO inside a throwaway container (`wimlib` + `xorriso`). The media matches the
host: amd64 on x86_64 hosts, arm64 on Apple Silicon and other arm64 hosts
(mastered UEFI-only — ARM has no BIOS boot path).

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
pulls the VirtIO driver ISO — on x86_64 also WinFSP — so the guest gets
paravirtualized disk and network out of the box (plus virtiofs on x86_64;
arm64 guests use an NVMe system disk and skip virtiofs until upstream ships a
signed ARM64 `viofs` — see
[docs/windows-guests.md → ARM64 guests](docs/windows-guests.md#arm64-guests)).

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

### Resizable BAR (optional)

A GPU bound to `vfio-pci` at boot keeps its firmware-default BAR (typically 256M), and the guest is stuck with it — gaming guests lose ReBAR/SAM, and compute stacks that map all VRAM CPU-visible (e.g. tinygrad) fail outright. `vee gpu rebar` installs a boot-time resize that runs before any driver binds:

```sh
vee gpu rebar 08:00.0             # show current + supported BAR sizes
vee gpu rebar 08:00.0 --size 16G  # resize BAR0 to 16G on every boot (reboot to apply)
```

See [docs/gpu-rebar.md](docs/gpu-rebar.md) for how it works and why runtime resizing is not safe on cards with reset quirks.

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
| `vee pull <distro> [version]` | Download or build a base image into the cache (`vee pull macos` fetches a macOS restore image) |
| `vee start <name>` | Boot a VM (detached by default) |
| `vee stop <name>` | Graceful shutdown |
| `vee list` | List all VMs and status |
| `vee status <name>` | Show detailed status of a VM |
| `vee ssh <name>` | Open a shell |
| `vee ssh-share <name>` | Share host SSH agent into the VM via AF_VSOCK |
| `vee tunnel <name> [service]` | List VM services, or open/connect to one |
| `vee ports <name>` | List bound TCP ports and process names in a running VM |
| `vee ip <name>` | Show a VM's IP addresses (guest agent, or host lease/ARP tables by MAC) |
| `vee logs <name>` | Stream QEMU output, or the helper log for a macOS guest |
| `vee monitor <name>` | Real-time CPU / memory / disk / network stats |
| `vee qmp <name> <command>` | Send a QMP (QEMU Machine Protocol) command to a running VM |
| `vee screenshot <name> [file.png]` | Capture a running VM's display to a PNG file (works headless; QEMU backend) |
| `vee view <name>` | Open or connect to a VM's display (SPICE, GPU, or Screen Sharing for a macOS guest) |
| `vee config <name>` | Edit a VM's configuration in an interactive TUI (`--ssh-port` changes the forwarded SSH port directly, live on a running VM) |
| `vee check <name>` | Run health checks on an installed VM |
| `vee network <name>` | Show firewall, VPN, DNS, route, and egress state of a running VM |
| `vee backup <name>` | Back up directories from a running VM |
| `vee autostart <name>` | Enable or disable autostart for a VM |
| `vee move <name> <target-dir>` | Move a VM's boot disk to another directory |
| `vee delete <name>...` | Wipe one or more VMs and all their disks |
| `vee daemon` | Run the vee daemon (starts and watches autostart VMs) |
| `vee dashboard` | Start a web dashboard for all VMs |
| `vee mcp` | Run an MCP server over stdio so coding agents can drive vee |
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

Some of these are QEMU-only, because they are built on QEMU interfaces a macOS
guest has no equivalent of: `vee monitor`, `vee qmp` and the dashboard's live
stats need QMP, and `vee check` and `vee ports` need the QEMU guest agent. See
[macOS guests](https://vee.benehiko.com/getting-started/macos-guests/) for the
full matrix of what that backend supports.

## MCP server (coding agents)

`vee mcp` speaks the [Model Context Protocol](https://modelcontextprotocol.io)
over stdio, exposing vee as typed tools so coding agents can manage VMs
without parsing CLI output:

```sh
claude mcp add vee -- vee mcp    # Claude Code; other MCP clients work the same way
```

The MCP surface has **feature parity with the CLI**: every command either
maps to tools (lifecycle, guest exec, ports, services, logs, QMP, stats,
backups, images, GPU inspection, runner credentials, pacman mirror) or
carries an explicit CLI-only exemption (interactive TUIs, root-mutating host
operations). A test walks the command tree and fails the build when the two
drift. A typical agent flow: `vm_create {template: "devbox", start: true}` →
`vm_exec {command: "docker run --rm alpine echo ok"}` → `vm_delete`. See
[docs/mcp.md](docs/mcp.md) for the full tool reference.

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

On an Apple Silicon Mac, `make build` and `make install` also build
`vee-vz-helper` — the binary that hosts Virtualization.framework guests — and
`make install` puts it beside `vee` in `~/.vee/bin` (`make vz-helper` does the
same on its own). It needs cgo (the Virtualization.framework bindings are
Objective-C) and is ad-hoc codesigned with the
`com.apple.security.virtualization` entitlement, which macOS honours without a
paid Apple Developer account. Only vz-backend guests (`--template macos`,
`--vz`) need it.

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
git tag -s v0.4.0
git push origin v0.4.0
```

It cross-compiles the `vee` binary for every supported host — `linux/amd64`,
`linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64` — with the tag,
commit, and build date injected via `-ldflags` (so `vee version` reports the
release identity). Each build is packaged as a `.tar.gz` (binary + `LICENSE` +
`README.md` + `THIRD_PARTY_LICENSES`), published twice with a `.sha256` beside
each: `vee-<tag>-<os>-<arch>.tar.gz` and an unversioned `vee-<os>-<arch>.tar.gz`,
so install docs can use a stable `releases/latest/download/…` URL. A GitHub
Release is published whose body lists the commits since the previous tag.
Tags containing a hyphen (e.g. `v0.4.0-rc1`) are marked as pre-releases.

The `darwin/arm64` archive additionally contains `vee-vz-helper`, built with cgo
and ad-hoc codesigned in the workflow; the release fails if the
`com.apple.security.virtualization` entitlement did not land, since macOS would
refuse to run the helper without it. When `docs/release-notes/<tag>.md` exists it
is prepended to the release body, so a release whose story is larger than its
commit subjects can lead with the story.

On Windows, VFIO passthrough, virtiofs, vsock, swtpm, bridge networking, and the
systemd daemon are Linux-only and degrade gracefully; the binary runs x86-64
guests under WHPX. See [docs/windows.md](docs/windows.md).

### Installing a release

Download the `.tar.gz` for your platform from the
[Releases page](https://github.com/Benehiko/vee/releases), verify it against the
matching `.sha256`, extract the `vee` binary onto your `PATH`, and install the
host QEMU packages. Full step-by-step instructions (Linux, macOS, Windows) plus
how to run vee as a daemon are in the
[Installation guide](https://vee.benehiko.com/getting-started/installation/).

Apple Silicon users who want macOS guests should install the tarball's second
binary, `vee-vz-helper`, into the same directory as `vee` (vee also accepts an
explicit path in `$VEE_VZ_HELPER`, and falls back to `~/.vee/bin` and `$PATH`).
A browser download arrives quarantined, which Gatekeeper would refuse to run;
vee clears that flag itself every time it resolves the helper, re-signing only if
the signature is missing the entitlement. No other platform ships the helper, and
no other guest needs it.

## Docs

The full documentation site is published at **<https://vee.benehiko.com/>**. Deep-dive
docs also live in this repo:

- [docs/prerequisites.md](docs/prerequisites.md) — system setup, groups, bridge networking, OVMF
- [docs/macos.md](docs/macos.md) — macOS on Apple Silicon: HVF host support, the per-guest GPU matrix, and macOS guests on Virtualization.framework
- [docs/linux-vz.md](docs/linux-vz.md) — Linux guests on Virtualization.framework (`--backend vz`): native vsock on macOS hosts, EFI or direct-kernel boot, raw disks
- [docs/windows.md](docs/windows.md) — Windows (WHPX) host support, feature matrix, and the nested-virtualization limitation
- [docs/windows-guests.md](docs/windows-guests.md) — the on-demand Windows guest ISO build pipeline
- [docs/windows-24h2-install.md](docs/windows-24h2-install.md) — full writeup of the Windows 11 24H2 install debugging
- [docs/qmp.md](docs/qmp.md) — `vee qmp`, `vee screenshot`, and the daemon-routed QMP transport
- [docs/mcp.md](docs/mcp.md) — the MCP server: tool reference and agent registration
- [docs/gpu-passthrough-gaming.md](docs/gpu-passthrough-gaming.md) — Sunshine + Moonlight streaming over GPU passthrough
- [docs/media-sources.md](docs/media-sources.md) — attaching NFS/SMB/host-dir/block/USB media to VMs
- [docs/pacman-mirror.md](docs/pacman-mirror.md) — host-side pacman caching proxy for Arch VMs
- [docs/host-shutdown.md](docs/host-shutdown.md) — how the daemon stops VMs cleanly at host shutdown (logind on Linux, launchd on macOS)
- [docs/install-iso-lifecycle.md](docs/install-iso-lifecycle.md) — one-shot installer ISO lifecycle and repair
- [docs/github-runner.md](docs/github-runner.md) — self-hosted GitHub Actions runner: cred persistence, SSH keys, disk GC
- [docs/docs-site.md](docs/docs-site.md) — Hugo documentation site: local preview and Cloudflare deploy

## License

MIT — see [LICENSE](LICENSE).
