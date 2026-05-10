---
title: Prerequisites
weight: 10
---

GPU passthrough requires IOMMU support in both the CPU and motherboard firmware, plus a few system-level configuration steps.

## 1. Enable IOMMU

Add the appropriate kernel parameter for your CPU:

**Intel:**
```
intel_iommu=on iommu=pt
```

**AMD:**
```
amd_iommu=on iommu=pt
```

Edit `/etc/default/grub`:

```
GRUB_CMDLINE_LINUX_DEFAULT="... amd_iommu=on iommu=pt"
```

Apply and reboot:

```sh
sudo update-grub   # Debian/Ubuntu
sudo grub-mkconfig -o /boot/grub/grub.cfg  # Arch
```

Verify IOMMU is active after reboot:

```sh
dmesg | grep -i iommu | head -5
```

## 2. Load vfio-pci

Create `/etc/modules-load.d/vfio.conf`:

```
vfio
vfio_iommu_type1
vfio_pci
```

## 3. Join the vfio group

```sh
sudo usermod -aG vfio $USER
```

Log out and back in. Verify:

```sh
groups | grep vfio
```

## 4. Set unlimited locked memory

VFIO DMA-maps the entire guest RAM. The default `memlock` limit causes QEMU to fail:

```
vfio_container_dma_map(...) = -12 (Cannot allocate memory)
```

Fix:

```sh
sudo tee /etc/security/limits.d/vee-vfio.conf <<'EOF'
* - memlock unlimited
EOF
```

Log out and back in. Verify:

```sh
ulimit -l   # should print "unlimited"
```

## 5. Bind the GPU to vfio-pci

```sh
# List GPUs and their IOMMU groups
vee gpu list

# Bind to vfio-pci (run as root)
sudo vee gpu bind 08:00.0

# Verify — all devices in the IOMMU group should show vfio-pci
vee gpu status 08:00.0 --memory 16G
```

All devices in the IOMMU group (GPU + audio function) must be bound to `vfio-pci` together.
