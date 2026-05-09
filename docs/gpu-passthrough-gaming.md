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
guest_agent: true
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

**First boot with monitor plugged in:** On the first boot after setting the
kernel parameter, plug a physical monitor into the GPU so amdgpu can initialize
the display engine. Subsequent boots work headlessly via the `video=` param.

### Sunshine

Install Sunshine for your distro (see [Sunshine docs](https://docs.lizardbyte.dev/projects/sunshine/)).

#### Configuration

Create `/home/<user>/.config/sunshine/sunshine.conf`:

```ini
encoder = vaapi          # use GPU hardware encoding (AMD: vaapi, NVIDIA: nvenc)
av1_mode = 0             # disable AV1 — causes session teardown deadlock on some AMD/vaapi builds
hevc_mode = 0            # disable HEVC — same issue; H.264 is stable
min_threads = 4
output_name = 0          # capture primary display (the GPU output)
qp = 28                  # encode quality (lower = better quality, higher bitrate)
```

> **Note on `av1_mode`/`hevc_mode`:** Some Sunshine nightly builds (e.g. `2025.x`)
> have a deadlock in session teardown when AV1 or HEVC encoding is used with
> vaapi on AMD GPUs. This causes `Fatal: Hang detected! Session failed to
> terminate in 10 seconds` followed by a core dump on every disconnect. Set
> both to `0` to force H.264 only until a fixed release is available.

> **Note on `vaapi_strict_rc_buffer`:** Can cause hangs on some AMD setups.
> Leave it out of the config unless you have a specific reason to enable it.

> **Note on `bitrate`:** The `bitrate` config option is not recognized by all
> Sunshine versions. Set the bitrate cap from Moonlight's client settings or
> via the Sunshine web UI at `https://localhost:47990` instead.

#### Systemd service override

Sunshine must start after Xorg has initialized the display. The default
`sleep 5` pre-start delay is not reliable. Override the service to poll
xrandr until the connector is ready, then set the target resolution:

Create `~/.config/systemd/user/sunshine.service`:

```ini
[Unit]
Description=Self-hosted game stream host for Moonlight
StartLimitIntervalSec=500
StartLimitBurst=5

[Service]
Environment=DISPLAY=:0
TimeoutStartSec=120
ExecStartPre=/bin/sh -c 'until xrandr 2>/dev/null | grep -q "^DisplayPort-0 connected"; do sleep 2; done'
ExecStartPre=/bin/sh -c 'xrandr --newmode "2560x1440_60.00" 312.25 2560 2752 3024 3488 1440 1443 1448 1493 -hsync +vsync 2>/dev/null; xrandr --addmode DisplayPort-0 "2560x1440_60.00" 2>/dev/null; xrandr --output DisplayPort-0 --mode "2560x1440_60.00"'
ExecStart=/usr/bin/sunshine
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=xdg-desktop-autostart.target
```

Replace `DisplayPort-0` and `2560x1440` with your connector name and resolution.

Apply:

```sh
systemctl --user daemon-reload
systemctl --user enable --now sunshine
```

Sunshine's web UI is available at `https://localhost:47990` for pairing and
configuration.

#### Disable the guest firewall

Sunshine uses several UDP ports for the video/audio stream. On Ubuntu the UFW
firewall is enabled by default and blocks these ports, causing `Initial Ping
Timeout` and session crashes. Since this is a LAN-only gaming VM, disable UFW:

```sh
sudo ufw disable
```

### qemu-guest-agent (recommended)

Install the guest agent so `vee ssh` can resolve the VM's IP without ARP and
`vee start` can probe readiness:

```sh
sudo apt install qemu-guest-agent
```

The agent is socket-activated and starts automatically when the VM is launched
with `guest_agent: true` in `vm.yaml`. No manual `systemctl enable` is needed.

## Connecting with Moonlight

1. Open Moonlight on your client device
2. Add the VM's IP address (`192.168.x.x`) as a host
3. Enter the pairing PIN shown in Moonlight into Sunshine's web UI at
   `https://<vm-ip>:47990`
4. Select the desktop app and connect

## Troubleshooting

### All connectors disconnected / no display output

The GPU display engine did not initialize. Causes:

- `video=` kernel param not set → add it as described above and reboot
- First boot after adding the param → plug a physical monitor in for the first
  boot, then it works headlessly after that
- GPU stuck in D3cold from a previous unclean exit → `vee` attempts reset
  automatically; if it fails, cold reboot the host

### Sunshine crashes on every Moonlight disconnect

Symptom: `Fatal: Hang detected! Session failed to terminate in 10 seconds`
followed by core dump.

Cause: deadlock in session teardown when AV1 or HEVC is used with vaapi on
some AMD GPU + Sunshine nightly build combinations.

Fix: set `av1_mode = 0` and `hevc_mode = 0` in `sunshine.conf` to force H.264.

### Moonlight: "Failed to initialize video capture/encoding" (Error 503)

Sunshine started before Xorg was ready. The systemd service override above
(polling xrandr) prevents this. If it still occurs, restart Sunshine manually:

```sh
systemctl --user restart sunshine
```

### Moonlight: "Starting RTSP handshake failed" (Error 110)

Firewall blocking Sunshine's stream ports. Disable UFW:

```sh
sudo ufw disable
```

### Moonlight reports slow connection

- Check client-side bitrate setting in Moonlight preferences
- Set bitrate via Sunshine web UI at `https://<vm-ip>:47990`
- Ensure both host and client are on wired ethernet

### vee ssh cannot resolve IP

The ARP table may not have an IPv4 entry yet. Ping the VM first:

```sh
ping -c1 <vm-ip> && vee ssh <name>
```

With `guest_agent: true` and `qemu-guest-agent` installed, `vee ssh` resolves
the IP via QGA without needing ARP.
