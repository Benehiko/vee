# GPU Passthrough Gaming — Sunshine + Moonlight

```
[ HOST ] ── VFIO ── [ GPU ] ── amdgpu ── [ GUEST ]
                                              │
                                         Sunshine (KMS capture)
                                              │
                                        Moonlight client
```

The `gaming-arch` template provisions an Arch Linux guest with KDE Plasma, Steam, and Sunshine pre-configured for GPU passthrough. Moonlight connects from any client device on the same network.

---

## Host requirements

- GPU bound to `vfio-pci` before any VM starts (see [prerequisites.md](prerequisites.md))
- All devices in the GPU's IOMMU group bound together:

```sh
vee gpu bind 0000:08:00.0
vee gpu bind 0000:08:00.1   # GPU audio — same IOMMU reset domain
```

- The gaming-arch VM must be started as root (required for driver rebind reset):

```sh
sudo vee start <name>
```

---

## VM configuration

```yaml
gpu:
  mode: passthrough
  pci_addr: "0000:08:00.0"
  extra_vfio_addrs:
    - "0000:08:00.1"                           # GPU audio — pass together
  rom_bar: true                                # expose VBIOS ROM BAR to guest
  rom_file: "/home/user/.vee/gpu-vbios.rom"   # VBIOS dump (see below)
  rebind_reset: true                           # soft-reset via driver cycle on start
  rebind_reset_driver: "amdgpu"
  anti_detect: true
```

`extra_vfio_addrs` ensures all devices in the IOMMU group are passed together — QEMU cannot take ownership of a group unless all its devices are bound.

---

## Dumping the GPU VBIOS

AMD Navi GPUs require `rom_bar: true` so the guest `amdgpu` driver can read the VBIOS and initialize display CRTCs. Supplying an explicit `rom_file` is more reliable than relying on the ROM BAR alone.

Dump the VBIOS while the GPU is bound to `amdgpu` (not `vfio-pci`):

```sh
# 1. Bind to amdgpu temporarily (GPU must not be in use by a VM)
sudo bash -c 'echo amdgpu > /sys/bus/pci/devices/0000:08:00.0/driver_override && echo 0000:08:00.0 > /sys/bus/pci/drivers_probe'

# 2. Dump via debugfs
sudo bash -c 'cat /sys/kernel/debug/dri/1/amdgpu_vbios > /home/user/.vee/gpu-vbios.rom'

# 3. Rebind to vfio-pci
sudo bash -c 'echo > /sys/bus/pci/devices/0000:08:00.0/driver_override && echo 0000:08:00.0 > /sys/bus/pci/drivers/amdgpu/unbind && echo 0000:08:00.0 > /sys/bus/pci/drivers/vfio-pci/bind'
```

Set `rom_file` in the VM config to the dump path.

> **TechPowerUp VGABIOS:** Downloaded ROM files from TechPowerUp are often empty (0 bytes) and unusable. Always dump from the live device via debugfs.

---

## Driver rebind reset (Navi31 / RDNA3)

AMD Navi31 (RX 7900 series) has no working Function Level Reset (FLR) and is not supported by `vendor-reset`. After a VM shuts down, the GPU is stuck in a corrupted power state and cannot be reused without a host cold reboot.

`rebind_reset: true` performs a `vfio-pci → amdgpu → vfio-pci` driver cycle before each VM start. The native `amdgpu` driver init acts as a soft reset. This is a best-effort workaround — success depends on hardware and BIOS version.

Requirements:
- `sudo vee start <name>` — rebind writes are root-only sysfs operations
- Cold reboot the host before the very first VM start after booting

**One session per boot:** If the soft reset fails (GPU enters a bad state after an unclean shutdown), a host cold reboot is the only recovery. The error message will say `VFIO device(s) stuck in D3cold`.

---

## Display output (HPD limitation)

VFIO does not relay Hot Plug Detect (HPD) signals from the host to the guest. The GPU's `amdgpu` driver starts without seeing any connected monitors — all connectors report `disconnected` and the display engine allocates no CRTCs.

The `gaming-arch` template adds `video=DP-1:e video=HDMI-A-1:e` to the kernel command line, forcing those connectors enabled at boot regardless of HPD state. Sunshine KMS capture then has an active CRTC to capture from.

To add or change forced connectors:

```sh
# Inside the VM
sudo sed -i 's/GRUB_CMDLINE_LINUX_DEFAULT="/GRUB_CMDLINE_LINUX_DEFAULT="video=DP-2:e /' /etc/default/grub
sudo grub-mkconfig -o /boot/grub/grub.cfg
sudo reboot
```

Verify after reboot:

```sh
cat /sys/class/drm/card1-DP-1/status   # should print: connected
```

---

## Post-install: fix Vulkan on Mesa 26

Arch Linux's Mesa 26 is built with `-D amdgpu-virtio=true`. This causes `radv` to route GPU initialization through a virtio-specific code path (`vdrm_device_connect`) that fails on VFIO-passed amdgpu devices. Vulkan returns zero physical devices and `vkcube` fails with:

```
MESA: error: vdrm_device_connect failed
radv/amdgpu: failed to initialize device
vkEnumeratePhysicalDevices reported zero accessible devices
```

There is no runtime workaround — Mesa must be rebuilt with `-D amdgpu-virtio=false`.

### Rebuild Mesa in the guest

```sh
# 1. Get the Arch mesa PKGBUILD
mkdir -p ~/mesa-vfio && cd ~/mesa-vfio
git clone --depth=1 https://gitlab.archlinux.org/archlinux/packaging/packages/mesa.git .

# 2. Disable amdgpu-virtio
sed -i 's/-D amdgpu-virtio=true/-D amdgpu-virtio=false/' PKGBUILD

# 3. Import the signing key
gpg --recv-keys 8D8E31AFC32428A6

# 4. Build and install (takes ~30 minutes)
makepkg -si --noconfirm
```

After install, verify Vulkan sees the GPU:

```sh
vulkaninfo --summary 2>&1 | grep deviceName
# Expected: AMD Radeon RX 7900 GRE (RADV NAVI31)
```

The patched mesa survives `pacman -Syu` until Arch updates the `mesa` package to a version that fixes the issue upstream. After any `mesa` upgrade, recheck with `vulkaninfo --summary` and rebuild if the error returns.

---

## Connect with Moonlight

```
1. Open Moonlight on your client device
2. Add the VM's IP: vee ip <name>
3. Enter the PIN shown in Moonlight into Sunshine's web UI:
   https://<vm-ip>:47991
4. Select the desktop and connect
```

Sunshine streams over the local network — no VPN or port forwarding required for LAN play.

---

## Troubleshooting

### VFIO device stuck in D3cold

```
Error: VFIO device(s) stuck in D3cold: 0000:08:00.0
```

GPU is in an unrecoverable power state. Cold reboot the host:

```sh
sudo reboot
```

After rebooting, run `sudo vee start <name>` — the rebind reset cycle runs automatically.

---

### All connectors disconnected / no CRTCs

```sh
sudo dmesg | grep "Cannot find any crtc"
```

Causes and fixes:

| Cause | Fix |
|---|---|
| `rom_bar: false` | Set `rom_bar: true` in config — guest amdgpu needs VBIOS to init CRTCs |
| `video=` param missing | Add `video=DP-1:e` (or HDMI-A-1) to GRUB cmdline, reboot |
| GPU not cold-booted | Cold reboot host, then `sudo vee start` |

---

### Sunshine fails to start — no encoder found

```
Fatal: Unable to find display or encoder during startup
```

Sunshine starts before the Wayland session is ready, or KMS has no active CRTC. Check:

```sh
vee ssh <name> -- 'for c in /sys/class/drm/card1-*/status; do echo "$c: $(cat $c)"; done'
```

If all connectors show `disconnected`, the display engine did not initialize — see the CRTCs section above.

If connectors are connected, restart Sunshine:

```sh
vee ssh <name> -- 'XDG_RUNTIME_DIR=/run/user/1000 systemctl --user restart sunshine'
```

---

### Vulkan: zero accessible devices

Mesa 26 `amdgpu-virtio` bug — see the [Post-install: fix Vulkan](#post-install-fix-vulkan-on-mesa-26) section above.

---

### Host mouse lag / high CPU during VM session

The guest `amdgpu` driver entered a GPU reset loop (`amdgpu-reset-dev` kworker). This is triggered by a GPU hang and burns ~25% host CPU per stuck worker.

Stop the VM:

```sh
vee stop <name>
```

Then cold reboot the host before restarting. This is a Navi31 hardware limitation — there is no in-session recovery.
