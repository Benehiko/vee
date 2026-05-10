# ▸ GPU PASSTHROUGH GAMING — SUNSHINE + MOONLIGHT

```
╔══════════════════════════════════════════════════════════════╗
║  STREAM THE GRID :: GPU PASSTHROUGH + GAME STREAMING         ║
║  Sunshine captures. Moonlight connects. You play.            ║
╚══════════════════════════════════════════════════════════════╝
```

---

## ▸ OVERVIEW

```
  [ HOST ] ──── VFIO ──── [ GPU ] ──── amdgpu/nvidia ──── [ GUEST ]
                                                               │
                                                          Sunshine
                                                               │
                                                         ══════╪══════
                                                         Moonlight client
```

- QEMU passes the GPU through to the guest via VFIO
- The guest runs `amdgpu` (or `nvidia`) as normal
- Sunshine captures the GPU-rendered desktop and streams it over the network
- Moonlight connects from any client device

No physical monitor required. The kernel `video=` parameter forces the display connector on so Sunshine always has a display to capture.

---

## ▸ HOST PREREQUISITES

See [prerequisites.md](prerequisites.md) for VFIO group setup, memlock limits, and the AMD Navi ROM BAR quirk.

Bind all devices in the GPU's IOMMU group to `vfio-pci`:

```sh
vee gpu bind 0000:08:00.0
vee gpu bind 0000:08:00.1   # GPU audio — must be bound together
```

Verify:

```sh
vee gpu status 0000:08:00.0
```

---

## ▸ VM CONFIGURATION (`vm.yaml`)

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

`extra_vfio_addrs` passes all devices in the IOMMU group together. Without it QEMU cannot take ownership of the group.

---

## ▸ VBIOS (`rom_file`)

AMD Navi GPUs (RX 6000 / 7000) return an invalid ROM signature when `vfio-pci` probes the ROM BAR — causing a 65-second reset hang. Supply a VBIOS dump to avoid it.

Download the correct ROM for your board from [TechPowerUp VGABIOS](https://www.techpowerup.com/vgabios/) and set `rom_file` in `vm.yaml`.

---

## ▸ GUEST SETUP

### Force display connector on (headless operation)

Without a physical monitor, the GPU display engine does not initialize and `amdgpu` reports no outputs. Force the connector on via kernel parameter.

Edit `/etc/default/grub` inside the VM:

```
GRUB_CMDLINE_LINUX_DEFAULT="... video=DP-1:2560x1440@60e"
```

The `e` suffix forces the connector enabled regardless of hotplug detect (HPD). Replace `DP-1` with your connector name (check `ls /sys/class/drm/`). Replace the resolution with your target streaming resolution.

Apply:

```sh
sudo update-grub
```

Reboot. Verify:

```sh
DISPLAY=:0 xrandr | grep connected
```

The connector should appear as `connected` with your target resolution.

> **First boot:** On the first boot after setting `video=`, plug a physical monitor into the GPU so `amdgpu` can initialize the display engine. Subsequent boots work headlessly.

---

### Sunshine

Install Sunshine for your distro — see the [Sunshine docs](https://docs.lizardbyte.dev/projects/sunshine/).

#### Configuration

Create `/home/<user>/.config/sunshine/sunshine.conf`:

```ini
encoder = vaapi          # GPU hardware encoding (AMD: vaapi, NVIDIA: nvenc)
av1_mode = 0             # disable AV1 — session teardown deadlock on some AMD/vaapi builds
hevc_mode = 0            # disable HEVC — same issue; H.264 is stable
min_threads = 4
output_name = 0          # capture primary display (the GPU output)
qp = 28                  # encode quality (lower = better quality, higher bitrate)
```

> **`av1_mode` / `hevc_mode`:** Some Sunshine nightly builds (`2025.x`) have a deadlock in session teardown when AV1 or HEVC is used with vaapi on AMD GPUs — `Fatal: Hang detected! Session failed to terminate in 10 seconds` followed by a core dump on every disconnect. Force H.264 (`0`) until a fixed release ships.

> **`vaapi_strict_rc_buffer`:** Can cause hangs on some AMD setups. Leave it out unless you have a specific reason.

> **`bitrate`:** Not recognized by all Sunshine versions. Set bitrate cap from Moonlight's client settings or via the Sunshine web UI at `https://localhost:47990`.

#### Systemd service override

Sunshine must start after Xorg has initialized the display. The default `sleep 5` pre-start delay is unreliable. Override the service to poll `xrandr` until the connector is ready, then set the target resolution.

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

Sunshine web UI: `https://localhost:47990` — pairing and configuration.

#### Disable the guest firewall

Sunshine uses several UDP ports for the video/audio stream. On Ubuntu, UFW blocks these by default — causing `Initial Ping Timeout` and session crashes. This is a LAN-only gaming VM:

```sh
sudo ufw disable
```

---

### qemu-guest-agent (recommended)

Install the guest agent so `vee ssh` can resolve the VM's IP without ARP and `vee start` can probe readiness:

```sh
sudo apt install qemu-guest-agent
```

The agent is socket-activated and starts automatically when the VM is launched with `guest_agent: true` in `vm.yaml`. No manual `systemctl enable` needed.

---

## ▸ CONNECT WITH MOONLIGHT

```
1 ── Open Moonlight on your client device
2 ── Add the VM's IP address as a host
3 ── Enter the pairing PIN shown in Moonlight into Sunshine's web UI
     https://<vm-ip>:47990
4 ── Select the desktop app and connect
```

---

## ▸ TROUBLESHOOTING

```
╔══════════════════════════════════════════════════════════════╗
║  FAULT DIAGNOSIS :: READ THE LOGS, TRACE THE SIGNAL          ║
╚══════════════════════════════════════════════════════════════╝
```

### GPU stuck in D3cold — QEMU crashes immediately

**Symptom:** `pci_irq_handler: Assertion '0 <= irq_num && irq_num < PCI_NUM_PINS' failed` in QEMU output, or `VFIO device D3cold reset failed` in `vee start` output.

**Cause:** The GPU has no power (D3cold state). This happens when a previous QEMU run crashed without releasing the device cleanly, or when the kernel's vfio-pci runtime PM autosuspended the device between runs.

**Prevention (permanent fix):** Tell vfio-pci never to autosuspend to D3cold:

```sh
echo 'options vfio-pci enable_runtime_pm=0' | sudo tee /etc/modprobe.d/vee-vfio.conf
# Arch:
sudo mkinitcpio -P
# Fedora/RHEL:
sudo dracut --regenerate-all --force
```

`vee daemon install` writes this file automatically (requires sudo prompt).

**Recovery (current session):** D3cold cannot be recovered without a full power cycle. If you see this error:

```sh
sudo reboot
```

After rebooting with `enable_runtime_pm=0` in place, the GPU will stay in D0 for the lifetime of the vfio-pci binding and the error will not recur.

---

### All connectors disconnected / no display output

GPU display engine did not initialize. Causes:

- `video=` kernel param not set → add it and reboot
- First boot after adding the param → plug a physical monitor in for the first boot; headless works after that
- GPU in D3cold → see the D3cold section above

---

### Sunshine crashes on every Moonlight disconnect

**Symptom:** `Fatal: Hang detected! Session failed to terminate in 10 seconds` followed by core dump.

**Cause:** Deadlock in session teardown when AV1 or HEVC is used with vaapi on some AMD GPU + Sunshine nightly combinations.

**Fix:** Set `av1_mode = 0` and `hevc_mode = 0` in `sunshine.conf` to force H.264.

---

### Moonlight: "Failed to initialize video capture/encoding" (Error 503)

Sunshine started before Xorg was ready. The systemd service override (polling `xrandr`) prevents this. If it still occurs, restart Sunshine manually:

```sh
systemctl --user restart sunshine
```

---

### Moonlight: "Starting RTSP handshake failed" (Error 110)

Firewall blocking Sunshine's stream ports:

```sh
sudo ufw disable
```

---

### Moonlight reports slow connection

- Check client-side bitrate setting in Moonlight preferences
- Set bitrate cap via Sunshine web UI at `https://<vm-ip>:47990`
- Ensure both host and client are on wired ethernet

---

### `vee ssh` cannot resolve IP

ARP table may not have an IPv4 entry yet. Ping first:

```sh
ping -c1 <vm-ip> && vee ssh <name>
```

With `guest_agent: true` and `qemu-guest-agent` installed, `vee ssh` resolves the IP via QGA without ARP.

---

```
[ STREAM ESTABLISHED ] :: SIGNAL STRONG :: LATENCY NOMINAL
```
