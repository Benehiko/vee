# GPU Passthrough Gaming (Sunshine + Moonlight)

This guide covers setting up a Linux gaming VM with GPU passthrough, a headless
virtual display, and game streaming via Sunshine and Moonlight.

## Overview

The setup works as follows:

- QEMU passes the GPU through to the guest via VFIO
- The guest runs `amdgpu` (or `nvidia`) as normal
- Sunshine captures the GPU-rendered desktop and streams it over the network
- Moonlight connects from any client device

No physical monitor needs to be permanently attached. The kernel `video=` parameter
forces the display connector on so Sunshine always has a display to capture.

## Host prerequisites

See [prerequisites.md](prerequisites.md) for VFIO group setup, memlock limits,
and the AMD Navi ROM BAR quirk.

Bind all devices in the GPU's IOMMU group to `vfio-pci`:

```sh
vee gpu bind 0000:08:00.0
vee gpu bind 0000:08:00.1   # GPU audio — must be bound together
```

Verify:

```sh
vee gpu status 0000:08:00.0
```

## VM configuration (vm.yaml)

```yaml
gpu:
  mode: passthrough
  pci_addr: "0000:08:00.0"
  extra_vfio_addrs:
    - "0000:08:00.1"          # GPU audio peer — same IOMMU reset domain
  rom_file: "/home/user/.vee/gpu.rom"   # Sapphire/AMD Navi VBIOS dump
  anti_detect: true

ssh_user: youruser
```

`extra_vfio_addrs` passes all devices in the IOMMU group through together.
Without it QEMU cannot take ownership of the group.

## VBIOS (rom_file)

AMD Navi GPUs (RX 6000/7000) return an invalid ROM signature when `vfio-pci`
tries to probe the ROM BAR. Supply a VBIOS dump to avoid the 65-second reset
hang. Download the correct ROM for your board from
[TechPowerUp VGABIOS](https://www.techpowerup.com/vgabios/) and set `rom_file`
in `vm.yaml`.

## Guest setup

### Force display connector on (required for headless)

Without a physical monitor, the GPU display engine does not initialize and
amdgpu reports no outputs. Add a kernel parameter to force the connector on:

Edit `/etc/default/grub` inside the VM:

```
GRUB_CMDLINE_LINUX_DEFAULT="... video=DP-1:2560x1440@60e"
```

The `e` suffix forces the connector enabled regardless of hotplug detect (HPD).
Replace `DP-1` with the connector your GPU uses (check `ls /sys/class/drm/`).
Replace the resolution with your target streaming resolution.

Apply:

```sh
sudo update-grub
```

Reboot the VM. After reboot, verify:

```sh
DISPLAY=:0 xrandr | grep connected
```

You should see the connector listed as `connected` with your target resolution.

### Sunshine

Install Sunshine for your distro (see [Sunshine docs](https://docs.lizardbyte.dev/projects/sunshine/)).

Recommended `/home/<user>/.config/sunshine/sunshine.conf`:

```ini
encoder = vaapi          # use GPU hardware encoding
av1_mode = 3
hevc_mode = 3
min_threads = 4
output_name = 0          # capture primary display (the GPU output)
qp = 10                  # encode quality (lower = better, higher bitrate)
bitrate = 50000          # cap at 50 Mbps — tune for your network
vaapi_strict_rc_buffer = enabled
```

Enable and start:

```sh
systemctl --user enable --now sunshine
```

Sunshine's web UI is available at `https://localhost:47990` for pairing and
configuration.

### qemu-guest-agent (recommended)

Install the guest agent so `vee ssh` can resolve the VM's IP without ARP and
`vee start` can probe readiness:

```sh
sudo apt install qemu-guest-agent
sudo systemctl enable --now qemu-guest-agent
```

Then add to `vm.yaml`:

```yaml
guest_agent: true
```

## Connecting with Moonlight

1. Open Moonlight on your client device
2. Add the VM's IP address (`192.168.x.x`) as a host
3. Enter the pairing PIN shown in Moonlight into Sunshine's web UI
4. Select the desktop app and connect

## Troubleshooting

### No display output / all connectors disconnected

The GPU was not detected with a monitor at boot. Causes:

- `video=` kernel param not set → add it as described above
- GPU stuck in D3cold from a previous unclean exit → `vee` will attempt reset
  automatically; if it fails, cold reboot the host

### Moonlight reports slow connection

- Sunshine bitrate is uncapped and `qp` is very low → add `bitrate = 50000`
  (or lower for WiFi clients) to `sunshine.conf`
- Check that both host and client are on wired ethernet for best results

### vee ssh cannot resolve IP

The ARP table may not have an IPv4 entry yet. Ping the VM first to populate it:

```sh
ping -c1 <vm-ip> && vee ssh <name>
```

With `guest_agent: true` and `qemu-guest-agent` running in the guest, `vee ssh`
resolves the IP via QGA without needing ARP.
