---
title: Troubleshooting
weight: 40
---

## vfio: Invalid PCI ROM header signature (0xffff)

```
vfio-pci 0000:08:00.0: Invalid PCI ROM header signature: expecting 0xaa55, got 0xffff
error getting device from group 22: No such device
```

AMD Navi (RDNA 2/3) ROM BAR quirk. Supply a VBIOS dump via `rom_file` in `vm.yaml`. See [AMD Navi ROM BAR Quirk]({{< relref "amd-navi-quirk" >}}).

## Permission denied /dev/vfio/N

User not in the `vfio` group:

```sh
sudo usermod -aG vfio $USER
```

Log out and back in.

## vfio_container_dma_map = -12 (ENOMEM)

The `memlock` limit is too low:

```sh
sudo tee /etc/security/limits.d/vee-vfio.conf <<'EOF'
* - memlock unlimited
EOF
```

Log out and back in. Verify with `ulimit -l`.

## Can't add chassis slot, error -16

Multiple VFIO devices with colliding PCIe root port slots. This is a vee bug — update to the latest version. vee assigns unique slots automatically.

## GPU stuck in D3cold

The GPU was left in `D3cold` by an unclean exit. vee attempts a PCI function-level reset automatically before each start. If it fails, cold reboot the host.

## All connectors disconnected / no display output

The GPU display engine did not initialize:

- `video=` kernel param not set → add it and reboot (see [Gaming Setup]({{< relref "gaming-setup" >}}))
- First boot after adding the param → plug a physical monitor in for the first boot
- GPU in D3cold → cold reboot the host

## Sunshine: "Failed to initialize video capture/encoding" (Error 503)

Sunshine started before Xorg was ready. The systemd service override (polling xrandr) prevents this. If it still occurs:

```sh
systemctl --user restart sunshine
```

## Sunshine crashes on every Moonlight disconnect

Symptom: `Fatal: Hang detected! Session failed to terminate in 10 seconds` + core dump.

Cause: deadlock in session teardown when AV1 or HEVC is used with vaapi on some AMD GPU + Sunshine nightly build combinations.

Fix: set `av1_mode = 0` and `hevc_mode = 0` in `sunshine.conf` to force H.264 only.

## Moonlight: "Starting RTSP handshake failed" (Error 110)

Firewall blocking Sunshine's stream ports:

```sh
sudo ufw disable
```

## Moonlight: slow connection / low bitrate

- Check client-side bitrate setting in Moonlight preferences
- Set bitrate cap via Sunshine web UI at `https://<vm-ip>:47990`
- Ensure both host and client are on wired ethernet

## vee ssh cannot resolve IP

ARP table may not have an IPv4 entry yet:

```sh
ping -c1 <vm-ip> && vee ssh <name>
```

With `guest_agent: true` and `qemu-guest-agent` installed, `vee ssh` resolves the IP via QGA without needing ARP.
