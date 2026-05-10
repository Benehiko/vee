---
title: Gaming Setup (Sunshine + Moonlight)
weight: 30
---

Set up a headless Linux gaming VM with GPU passthrough, a virtual display, and game streaming via Sunshine and Moonlight. No physical monitor needs to be permanently attached.

## How it works

1. QEMU passes the GPU to the guest via VFIO
2. The guest runs `amdgpu` (or `nvidia`) as normal
3. A kernel parameter forces the display connector on so Sunshine always has a display to capture
4. Sunshine streams the GPU-rendered desktop over the network
5. Moonlight connects from any client device

## Host prerequisites

See [Prerequisites]({{< relref "prerequisites" >}}) for IOMMU, vfio group, and memlock setup.

Bind all devices in the GPU's IOMMU group to `vfio-pci`:

```sh
vee gpu bind 0000:08:00.0
vee gpu bind 0000:08:00.1   # GPU audio — must be bound together
```

## vm.yaml

```yaml
gpu:
  mode: passthrough
  pci_addr: "0000:08:00.0"
  extra_vfio_addrs:
    - "0000:08:00.1"          # GPU audio peer — same IOMMU reset domain
  rom_file: "/home/user/.vee/gpu.rom"   # required for AMD Navi
  anti_detect: true

ssh_user: youruser
guest_agent: true
```

`extra_vfio_addrs` passes all devices in the IOMMU group through together.
Without it QEMU cannot take ownership of the group.

For AMD Navi GPUs, `rom_file` is required — see [AMD Navi ROM BAR Quirk]({{< relref "amd-navi-quirk" >}}).

## Guest: force display connector on

Without a physical monitor the GPU display engine does not initialize and amdgpu
reports no outputs. Force the connector on with a kernel parameter:

Edit `/etc/default/grub` inside the VM:

```
GRUB_CMDLINE_LINUX_DEFAULT="... video=DP-1:2560x1440@60e"
```

The `e` suffix forces the connector enabled regardless of hotplug detect (HPD).
Replace `DP-1` with the connector your GPU uses (check `ls /sys/class/drm/`).

Apply and reboot:

```sh
sudo update-grub
```

After reboot, verify:

```sh
DISPLAY=:0 xrandr | grep connected
```

> **First boot:** On the first boot after adding the parameter, plug a physical
> monitor into the GPU so amdgpu can initialize the display engine. Subsequent
> boots work headlessly via the `video=` param.

## Guest: install Sunshine

Install Sunshine for your distro — see the [Sunshine documentation](https://docs.lizardbyte.dev/projects/sunshine/).

### sunshine.conf

Create `~/.config/sunshine/sunshine.conf`:

```ini
encoder = vaapi          # AMD: vaapi  NVIDIA: nvenc
av1_mode = 0             # disable AV1 — deadlock on some AMD/vaapi builds
hevc_mode = 0            # disable HEVC — same issue; H.264 is stable
min_threads = 4
output_name = 0          # capture primary display
qp = 28
```

> **av1_mode / hevc_mode:** Some Sunshine nightly builds have a deadlock in
> session teardown when AV1 or HEVC encoding is used with vaapi on AMD GPUs.
> Symptom: `Fatal: Hang detected! Session failed to terminate in 10 seconds`
> followed by a core dump on every disconnect. Set both to `0` to force H.264
> until a fixed release is available.

### Systemd service override

Sunshine must start after Xorg has initialized the display. Override the default
service to poll xrandr until the connector is ready, then set the target
resolution.

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

### Disable the guest firewall

Sunshine uses several UDP ports for the video/audio stream. On Ubuntu, UFW is
enabled by default and blocks these ports, causing `Initial Ping Timeout` and
session crashes. Since this is a LAN-only gaming VM, disable UFW:

```sh
sudo ufw disable
```

## Guest: install qemu-guest-agent

```sh
sudo apt install qemu-guest-agent
```

The agent is socket-activated and starts automatically when the VM is launched
with `guest_agent: true` in `vm.yaml`. No manual `systemctl enable` is needed.

## Connecting with Moonlight

1. Open Moonlight on your client device
2. Add the VM's IP address as a host
3. Enter the pairing PIN shown in Moonlight into Sunshine's web UI at `https://<vm-ip>:47990`
4. Select the desktop app and connect

Sunshine's web UI is also available at `https://localhost:47990` from inside the VM for pairing and configuration.
