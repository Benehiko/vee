# Prerequisites

## Required packages

| Package | Purpose |
|---------|---------|
| `qemu-system-x86_64` | VM execution engine |
| `qemu-img` | Disk image creation |
| `ovmf` | UEFI firmware (OVMF_CODE / OVMF_VARS) |
| `openssh` | `vee ssh` and `vee tunnel` |
| `virtiofsd` | Host directory sharing into VMs (`--virtiofs-dir`) |
| `swtpm` | TPM 2.0 emulation (Windows template) |

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

## KVM access

Your user must be in the `kvm` group to run hardware-accelerated VMs:

```sh
sudo usermod -aG kvm $USER
```

Log out and back in (or `newgrp kvm`) for the change to take effect.

## Disk passthrough (TrueNAS and raw block devices)

To pass host block devices directly into a VM (e.g. for TrueNAS ZFS data drives), your user must be in the `disk` group:

```sh
sudo usermod -aG disk $USER
```

Log out and back in (or `newgrp disk`) for the change to take effect.

Without this, QEMU will fail with `Permission denied` when opening devices under `/dev/disk/by-id/`.

## Bridge networking

Bridge-mode VMs (TrueNAS, gaming) require a host bridge interface. The default bridge name is `br0`.

### Create a persistent bridge (systemd-networkd)

Create `/etc/systemd/network/20-br0.netdev`:

```ini
[NetDev]
Name=br0
Kind=bridge
```

Create `/etc/systemd/network/21-br0-bind.network` (replace `enp6s0` with your interface):

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

Then enable and start:

```sh
sudo systemctl enable --now systemd-networkd
sudo networkctl reload
```

### Allow QEMU bridge access

QEMU needs permission to attach to the bridge without root. Add `br0` to `/etc/qemu/bridge.conf`:

```
allow br0
```

And ensure `/usr/lib/qemu/qemu-bridge-helper` is setuid:

```sh
sudo chmod u+s /usr/lib/qemu/qemu-bridge-helper
```

## GPU passthrough (VFIO)

To pass a GPU through to a VM using VFIO, two system-level changes are required.

### VFIO group membership

Your user must be in the `vfio` group so QEMU can open `/dev/vfio/<group>`:

```sh
sudo usermod -aG vfio $USER
```

Log out and back in (or `newgrp vfio`) for the change to take effect.

### Locked memory limit (memlock)

VFIO DMA-maps the entire guest RAM into the IOMMU. The default `memlock` limit
(typically 32 MiB) is far too small and causes QEMU to fail with:

```
vfio_container_dma_map(...) = -12 (Cannot allocate memory)
```

Set the limit to unlimited for all users:

```sh
sudo tee /etc/security/limits.d/vee-vfio.conf <<'EOF'
* - memlock unlimited
EOF
```

Log out and back in for PAM to apply the new limit. Verify with `ulimit -l`
(should print `unlimited`).

> **Note:** `vee` will attempt to raise the memlock limit on the QEMU child
> process automatically. If the system hard limit is still capped, it logs a
> warning and uses the maximum available — which may still be insufficient for
> large VMs.

## Shell completion

Register tab completion for your shell so `vee start <TAB>` completes VM names:

```sh
# bash — add to ~/.bashrc
source <(vee completion bash)

# zsh — add to ~/.zshrc
source <(vee completion zsh)

# fish — add to ~/.config/fish/config.fish
vee completion fish | source
```
