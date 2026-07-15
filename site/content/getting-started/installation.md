---
title: Installation
weight: 10
---

There are two ways to install vee:

1. **Download a prebuilt binary** from the GitHub Releases page (fastest — no Go toolchain needed).
2. **Build from source** (requires Go 1.21 or later).

Either way, vee drives QEMU, so the host QEMU packages listed under
[Prerequisites](#prerequisites) must be installed first.

## Download a prebuilt binary

Prebuilt `vee` binaries are published for every supported host on the
[Releases page](https://github.com/Benehiko/vee/releases). Each release ships a
`.tar.gz` per platform plus a matching `.sha256` checksum file:

| Platform | Asset |
|----------|-------|
| Linux (x86-64) | `vee-linux-amd64.tar.gz` |
| Linux (ARM64) | `vee-linux-arm64.tar.gz` |
| macOS (Apple Silicon) | `vee-darwin-arm64.tar.gz` |
| macOS (Intel) | `vee-darwin-amd64.tar.gz` |
| Windows (x86-64) | `vee-windows-amd64.tar.gz` |

Each snippet below grabs the **latest** release. For a specific version, swap
`latest/download` for `download/<tag>` (e.g. `download/v0.2.0`), and pick the
matching asset from the table above.

### Linux

```sh
curl -LO https://github.com/Benehiko/vee/releases/latest/download/vee-linux-amd64.tar.gz
curl -LO https://github.com/Benehiko/vee/releases/latest/download/vee-linux-amd64.tar.gz.sha256
sha256sum -c vee-linux-amd64.tar.gz.sha256
tar xzf vee-linux-amd64.tar.gz
install -Dm755 vee "$HOME/.vee/bin/vee"
```

### macOS

```sh
curl -LO https://github.com/Benehiko/vee/releases/latest/download/vee-darwin-arm64.tar.gz
curl -LO https://github.com/Benehiko/vee/releases/latest/download/vee-darwin-arm64.tar.gz.sha256
shasum -a 256 -c vee-darwin-arm64.tar.gz.sha256
tar xzf vee-darwin-arm64.tar.gz
install -Dm755 vee "$HOME/.vee/bin/vee"
xattr -d com.apple.quarantine "$HOME/.vee/bin/vee" 2>/dev/null || true   # unsigned binary
```

### Windows (PowerShell)

```powershell
$Base = "https://github.com/Benehiko/vee/releases/latest/download"
iwr "$Base/vee-windows-amd64.tar.gz"        -OutFile vee.tar.gz
iwr "$Base/vee-windows-amd64.tar.gz.sha256" -OutFile vee.tar.gz.sha256

# Verify the checksum:
if ((Get-Content vee.tar.gz.sha256).Split()[0].ToLower() -ne (Get-FileHash vee.tar.gz -Algorithm SHA256).Hash.ToLower()) { throw "checksum mismatch" }

tar xzf vee.tar.gz   # tar ships with Windows 10+
New-Item -ItemType Directory -Force "$HOME\.vee\bin" | Out-Null
Move-Item -Force vee.exe "$HOME\.vee\bin\vee.exe"
```

Then add `~/.vee/bin` to your `PATH` (next section) and jump to
[Verify](#verify). If you plan to run VMs on boot, see
[Run vee as a daemon](#run-vee-as-a-daemon).

## Prerequisites

Install the required system packages before building.

{{< hint info >}}
**Managed QEMU.** For platforms where vee publishes a `vee-qemu` bundle, vee
downloads a pinned, checksum-verified QEMU into `~/.vee/bin/` on first use and
prefers it over any system QEMU. That bundle also carries the edk2/OVMF firmware
under `~/.vee/share/qemu/`, so neither QEMU nor OVMF needs a system install. When
no bundle is published for your platform, vee falls back to the system QEMU on
your `PATH` — install the packages below.
{{< /hint >}}

### Arch Linux

```sh
sudo pacman -S qemu-desktop edk2-ovmf openssh virtiofsd swtpm
```

### Ubuntu / Debian

```sh
sudo apt install qemu-system-x86 ovmf openssh-client virtiofsd swtpm
```

### Fedora

```sh
sudo dnf install qemu-kvm edk2-ovmf openssh-clients virtiofsd swtpm
```

| Package | Purpose |
|---------|---------|
| `qemu-system-x86_64` | VM execution engine |
| `qemu-img` | Disk image creation |
| `ovmf` | UEFI firmware |
| `openssh` | `vee ssh` and `vee tunnel` |
| `virtiofsd` | Host directory sharing into VMs — optional, see below |
| `swtpm` | TPM 2.0 emulation (Windows template) |

{{< hint info >}}
**OVMF firmware location.** Distros ship OVMF under different names and
directories — Arch puts it in `/usr/share/OVMF/x64/OVMF_*.4m.fd`,
Debian/Ubuntu/Mint in `/usr/share/OVMF/OVMF_*_4M.fd`, Fedora/RHEL in
`/usr/share/edk2/ovmf/`. vee probes all of these automatically, so installing
your distro's OVMF package (`ovmf` on Debian-family, `edk2-ovmf` on Arch/Fedora)
is all that's needed. If your firmware lives somewhere unusual, override
`ovmf_code_path` / `ovmf_vars_path` in `~/.vee/config.yaml`.

When vee uses a managed QEMU bundle (see the note above), that bundle ships
edk2/OVMF firmware under `~/.vee/share/qemu/`, which vee prefers over any system
OVMF — so no distro OVMF package is required in that case.
{{< /hint >}}

{{< hint info >}}
**`virtiofsd` is optional.** When a VM first requests a virtiofs share and no
system `virtiofsd` is found, vee builds a pinned, checksum-verified copy on
demand into `~/.vee/bin/virtiofsd` — inside a host container (`nerdctl`/`docker`)
if one is available, otherwise inside a temporary Ubuntu VM. Installing the
distro package skips this on-demand build.
{{< /hint >}}

## KVM access

Your user must be in the `kvm` group to run hardware-accelerated VMs:

```sh
sudo usermod -aG kvm $USER
```

Log out and back in (or `newgrp kvm`) for the change to take effect.

## Build and install

```sh
git clone https://github.com/Benehiko/vee.git
cd vee
make install
```

This builds the binary and installs it to `~/.vee/bin/vee`. Add that directory to your `PATH`:

```sh
echo 'export PATH="$HOME/.vee/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```

## Shell completion

```sh
# bash — add to ~/.bashrc
source <(vee completion bash)

# zsh — add to ~/.zshrc
source <(vee completion zsh)

# fish — add to ~/.config/fish/config.fish
vee completion fish | source
```

## Verify

```sh
vee --help
```

## Run vee as a daemon

The daemon starts every VM marked `autostart=true` on boot and restarts any
that exit. It runs as a systemd **system** service (`/etc/systemd/system/vee.service`)
so it can react to logind's shutdown signal in time to stop VMs cleanly.
This is Linux-only.

Install and enable it (prompts for `sudo` — it writes a system unit and runs
`systemctl enable --now`):

```sh
vee daemon install
```

Check status, logs, and control the service with the usual systemctl verbs:

```sh
systemctl status vee            # is it running?
journalctl -u vee -f            # follow logs
sudo systemctl restart vee      # restart (e.g. after upgrading the vee binary)
sudo systemctl stop vee         # stop
```

{{< hint info >}}
**After upgrading vee, restart the daemon** so it runs the new binary:
`sudo systemctl restart vee`. The unit's `ExecStart` is pinned to the absolute
path of the `vee` binary that ran `vee daemon install`. If your upgrade
**replaces that same file in place** (a new release extracted over
`~/.vee/bin/vee`, or `make install`), a restart is all you need. If the new
binary lands at a **different path**, re-run `vee daemon install` so the unit
points at it.
{{< /hint >}}

Remove the service with:

```sh
vee daemon uninstall
```
