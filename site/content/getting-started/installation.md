---
title: Installation
weight: 10
---

vee is installed from source. It requires Go 1.21 or later.

## Prerequisites

Install the required system packages before building.

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
| `virtiofsd` | Host directory sharing into VMs |
| `swtpm` | TPM 2.0 emulation (Windows template) |

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
