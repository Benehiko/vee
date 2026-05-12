package templates

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

// GPUVendor describes the guest GPU type for driver selection.
type GPUVendor string

const (
	GPUVendorAMD    GPUVendor = "amd"
	GPUVendorNvidia GPUVendor = "nvidia"
	GPUVendorVirtio GPUVendor = "virtio"
)

// GamingOptions holds optional overrides for gaming VM creation.
type GamingOptions struct {
	// VirtiofsMountDir shares a host games directory (empty = skip).
	VirtiofsMountDir string
	// GPUVendor selects guest GPU driver packages (amd, nvidia, virtio).
	// Defaults to "amd" when unset.
	GPUVendor GPUVendor
	// Passthrough enables GPU passthrough mode with KasmVNC for browser access.
	// When false, virgl / virtio-vga-gl is used (no PCI passthrough required).
	Passthrough bool
	// PCIAddr is required when Passthrough is true (e.g. "08:00.0").
	PCIAddr string
	// NICMode selects networking: "bridge" (default) or "user".
	// Use "user" with Headless+SSHPort for e2e testing without a bridge interface.
	NICMode string
	// Bridge NIC bridge interface (default "br0", only used when NICMode="bridge").
	Bridge string
	// MAC address for the bridge NIC (empty = let QEMU pick).
	MAC string
	// Headless suppresses the display window; SSH-only access.
	Headless bool
	// SSHPort is the host port forwarded to guest port 22 (user-mode NIC only).
	SSHPort int
}

// NewGamingArchConfig builds a VMConfig for an Arch Linux gaming VM.
// Arch + KDE Plasma + Wayland is used as the base; cloud-init handles setup.
func NewGamingArchConfig(ctx context.Context, p provider.Provider, name string, sshKeys []string, opts GamingOptions) (*vm.VMConfig, error) {
	if opts.GPUVendor == "" {
		opts.GPUVendor = GPUVendorAMD
	}
	if opts.Bridge == "" {
		opts.Bridge = "br0"
	}

	img, err := images.NewImage(p, images.DistroArch, "latest")
	if err != nil {
		return nil, fmt.Errorf("gaming-arch image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("gaming-arch image download: %w", err)
	}

	conf := p.Config()
	vmDir := filepath.Join(conf.StoragePath, name)

	user := "gamer"
	writeFiles, runCmds := archGamingSetup(user, sshKeys, name, opts)

	gpuMode := vm.GPUVirtio
	gpuCfg := vm.GPUConfig{Mode: gpuMode}
	if opts.Passthrough {
		gpuCfg = vm.GPUConfig{
			Mode:       vm.GPUPassthrough,
			PCIAddr:    opts.PCIAddr,
			AntiDetect: true,
		}
	}

	nicMode := opts.NICMode
	if nicMode == "" {
		nicMode = "bridge"
	}

	sshPort := opts.SSHPort
	if nicMode == "user" && sshPort == 0 {
		sshPort = deterministicSSHPort(name)
	}

	diskPath := filepath.Join(vmDir, "storage", "disk-os.qcow2")
	cfg := &vm.VMConfig{
		Name:     name,
		Template: "gaming-arch",
		Memory:   "16G",
		CPUs:     8,
		Sockets:  1,
		Cores:    8,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:   nicMode,
			Bridge: opts.Bridge,
			Model:  "virtio-net-pci",
			MAC:    opts.MAC,
		},
		GPU:      gpuCfg,
		Headless: opts.Headless,
		SSHPort:  sshPort,
		UEFI:     vm.UEFIConfig{Enabled: true},
		Disks: []vm.DiskConfig{
			{
				Path:      diskPath,
				Size:      "80G",
				Format:    "qcow2",
				Interface: "virtio",
				Media:     "disk",
				Cache:     "writeback",
			},
			{
				Path:       img.AbsolutePath(),
				Interface:  "ide",
				Media:      "cdrom",
				Cache:      "none",
				Readonly:   true,
				InstallISO: true,
			},
		},
		CloudInit: &vm.CloudInitConfig{
			Hostname:   name,
			User:       user,
			SSHKeys:    sshKeys,
			RunCmds:    runCmds,
			WriteFiles: writeFiles,
		},
		SSHUser:   user,
		RTC:       "base=localtime,clock=host",
		CreatedAt: time.Now(),
	}

	// SPICE is always the display path for gaming-arch — either directly for
	// virtio GPU, or as the KasmVNC capture surface for passthrough GPU.
	cfg.SPICE = &vm.SPICEConfig{Port: 0, DisableTicketing: true}
	cfg.Services = []vm.ServiceEntry{
		{Name: "spice", Port: 0, Protocol: vm.ServiceSPICE}, // port filled by manager
	}

	if opts.Passthrough {
		// Add a secondary virtio-gpu alongside the VFIO GPU for SPICE/KasmVNC.
		cfg.VGA = "none"
		cfg.ExtraDevices = []string{"virtio-gpu-pci,edid=on,xres=1920,yres=1080"}
		cfg.Services = append(cfg.Services, vm.ServiceEntry{
			Name: "kasmvnc", Port: 8443, Protocol: vm.ServiceHTTPS,
		})
	}

	if opts.VirtiofsMountDir != "" {
		cfg.VirtiofsMounts = []vm.VirtiofsMount{
			{SharedDir: opts.VirtiofsMountDir, Tag: "Games"},
		}
	}

	return cfg, nil
}

// NewGamingBazziteConfig builds a VMConfig for a Bazzite gaming VM.
// Bazzite is an immutable Fedora Atomic derivative with Steam + Proton pre-installed.
// It boots from the Bazzite ISO directly — no cloud-init (Bazzite uses its own installer).
func NewGamingBazziteConfig(ctx context.Context, p provider.Provider, name string, opts GamingOptions) (*vm.VMConfig, error) {
	if opts.GPUVendor == "" {
		opts.GPUVendor = GPUVendorAMD
	}
	if opts.Bridge == "" {
		opts.Bridge = "br0"
	}

	img, err := images.NewImage(p, images.DistroBazzite, "latest")
	if err != nil {
		return nil, fmt.Errorf("gaming-bazzite image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("gaming-bazzite image download: %w", err)
	}

	conf := p.Config()
	vmDir := filepath.Join(conf.StoragePath, name)

	gpuCfg := vm.GPUConfig{Mode: vm.GPUVirtio}
	if opts.Passthrough {
		gpuCfg = vm.GPUConfig{
			Mode:       vm.GPUPassthrough,
			PCIAddr:    opts.PCIAddr,
			AntiDetect: true,
		}
	}

	diskPath := filepath.Join(vmDir, "storage", "disk-os.qcow2")
	cfg := &vm.VMConfig{
		Name:     name,
		Template: "gaming-bazzite",
		Memory:   "16G",
		CPUs:     8,
		Sockets:  1,
		Cores:    8,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:   "bridge",
			Bridge: opts.Bridge,
			Model:  "virtio-net-pci",
			MAC:    opts.MAC,
		},
		GPU:  gpuCfg,
		UEFI: vm.UEFIConfig{Enabled: true},
		Disks: []vm.DiskConfig{
			{
				// Install target disk — Bazzite installer writes here.
				Path:      diskPath,
				Size:      "80G",
				Format:    "qcow2",
				Interface: "virtio",
				Media:     "disk",
				Cache:     "writeback",
			},
			{
				// Boot from the Bazzite ISO for first-time install.
				Path:       img.AbsolutePath(),
				Format:     "raw",
				Interface:  "ide",
				Media:      "cdrom",
				Readonly:   true,
				InstallISO: true,
			},
		},
		RTC:       "base=localtime,clock=host",
		CreatedAt: time.Now(),
	}

	if opts.Passthrough {
		cfg.VGA = "none"
		cfg.ExtraDevices = []string{"virtio-gpu-pci,edid=on,xres=1920,yres=1080"}
		cfg.SPICE = &vm.SPICEConfig{Port: 0, DisableTicketing: true}
		cfg.Services = []vm.ServiceEntry{
			{Name: "spice", Port: 0, Protocol: vm.ServiceSPICE},
			{Name: "kasmvnc", Port: 8443, Protocol: vm.ServiceHTTPS},
		}
	}

	if opts.VirtiofsMountDir != "" {
		cfg.VirtiofsMounts = []vm.VirtiofsMount{
			{SharedDir: opts.VirtiofsMountDir, Tag: "Games"},
		}
	}

	return cfg, nil
}

// archGamingSetup returns cloud-init write_files and runcmd for Arch gaming VMs.
//
// The live Arch ISO has only a 256M tmpfs root — package installation must
// target the real disk via pacstrap + arch-chroot, not the live environment.
// All configuration is written into the chroot; the VM powers off at the end
// so vee can eject the ISO and boot from the installed disk.
func archGamingSetup(user string, sshKeys []string, hostname string, opts GamingOptions) ([]vm.CloudInitWriteFile, []string) {
	gpuPkgs := "mesa lib32-mesa vulkan-radeon lib32-vulkan-radeon vulkan-icd-loader lib32-vulkan-icd-loader vulkan-tools"
	if !opts.Passthrough {
		gpuPkgs += " vulkan-virtio lib32-vulkan-mesa-layers"
	}
	switch opts.GPUVendor {
	case GPUVendorNvidia:
		gpuPkgs = "nvidia nvidia-utils lib32-nvidia-utils vulkan-icd-loader lib32-vulkan-icd-loader vulkan-tools"
	}

	kasmvncService := ""
	kasmvncSetup := ""
	if opts.Passthrough {
		kasmvncService = fmt.Sprintf(`cat > /mnt/etc/systemd/system/kasmvnc.service <<'SVCEOF'
[Unit]
Description=KasmVNC remote desktop server
After=sddm.service

[Service]
Type=simple
User=%s
ExecStart=/usr/bin/Xvnc :1 -interface 0.0.0.0 -websocketPort 8443 -cert /etc/ssl/certs/ca-certificates.crt -SecurityTypes None
Restart=on-failure

[Install]
WantedBy=multi-user.target
SVCEOF`, user)

		kasmvncSetup = fmt.Sprintf(`
arch-chroot /mnt pacman -S --noconfirm git base-devel
arch-chroot /mnt sudo -u %[1]s bash -c 'cd /tmp && git clone https://aur.archlinux.org/yay.git && cd yay && makepkg -si --noconfirm'
# kasmvnc AUR package name varies by release; try known names, skip if none found.
for _pkg in kasmvnc kasmvncserver-bin kasmvnc-bin; do
  if arch-chroot /mnt sudo -u %[1]s yay -Si --aur "$_pkg" &>/dev/null; then
    arch-chroot /mnt sudo -u %[1]s yay -S --noconfirm "$_pkg" && break
  fi
done
if arch-chroot /mnt which Xvnc &>/dev/null; then
  arch-chroot /mnt sudo -u %[1]s bash -c 'mkdir -p ~/.vnc && kasmvncpasswd -w vee -u %[1]s'
  arch-chroot /mnt systemctl enable kasmvnc
else
  echo "==> kasmvnc not installed (no matching AUR package); skipping VNC setup"
fi`, user)
	}

	nvidiaSetup := ""
	if opts.GPUVendor == GPUVendorNvidia {
		nvidiaSetup = `arch-chroot /mnt systemctl enable nvidia-persistenced`
	}

	installScript := fmt.Sprintf(`#!/bin/bash
# Trace every command so cloud-init-output.log captures progress; redirect
# everything to a dedicated install log too, in case cloud-init buffers.
exec > >(tee -a /var/log/vee-install.log) 2>&1
set -euxo pipefail
DISK=/dev/vda
USER=%s

# On failure, surface the line + unwind any mounts so a subsequent re-run
# (manual or vee restart) is not blocked by stale partitions on $DISK.
on_err() {
  rc=$?
  echo "==> install.sh failed at line $1 (exit $rc)"
  umount -R /mnt 2>/dev/null || true
  exit $rc
}
trap 'on_err $LINENO' ERR

# Wait for time sync — the live ISO boots with the clock at 1970-01-01,
# which makes every HTTPS cert look "not yet valid" and breaks pacman /
# reflector. Force NTP up before any network fetch.
timedatectl set-ntp true || true
for i in $(seq 1 30); do
  if timedatectl show -p NTPSynchronized --value | grep -q '^yes$'; then
    break
  fi
  sleep 2
done
echo "==> clock: $(date -u '+%%Y-%%m-%%dT%%H:%%M:%%SZ')"

# Wait for a default route + working DNS — runcmd fires before
# network-online.target on some images.
for i in $(seq 1 30); do
  if ip route show default | grep -q '^default ' && getent hosts archlinux.org >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

# Partition: 512M EFI + rest as root
parted -s "$DISK" mklabel gpt
parted -s "$DISK" mkpart ESP fat32 1MiB 513MiB
parted -s "$DISK" set 1 esp on
parted -s "$DISK" mkpart primary ext4 513MiB 100%%

mkfs.fat -F32 "${DISK}1"
mkfs.ext4 -F "${DISK}2"

mount "${DISK}2" /mnt
mkdir -p /mnt/boot/efi
mount "${DISK}1" /mnt/boot/efi

# Pick fastest mirrors before pacstrap. Reflector reaches out over HTTPS
# only — rsync rate-tests routinely time out from this network. We write
# to a tempfile so a partial/failed reflector run never leaves an empty
# /etc/pacman.d/mirrorlist (which makes pacman -Sy fail with
# "no servers configured for repository").
pacman -Sy --noconfirm reflector
cp /etc/pacman.d/mirrorlist /etc/pacman.d/mirrorlist.iso
if reflector --protocol https --latest 20 --sort rate \
     --save /etc/pacman.d/mirrorlist.new 2>&1 \
   && [ -s /etc/pacman.d/mirrorlist.new ] \
   && grep -q '^Server = ' /etc/pacman.d/mirrorlist.new; then
  mv /etc/pacman.d/mirrorlist.new /etc/pacman.d/mirrorlist
  echo "==> reflector picked $(grep -c '^Server = ' /etc/pacman.d/mirrorlist) mirrors"
else
  echo "==> reflector failed or produced empty mirrorlist; keeping ISO mirrorlist"
  rm -f /etc/pacman.d/mirrorlist.new
  cp /etc/pacman.d/mirrorlist.iso /etc/pacman.d/mirrorlist
fi

# Enable multilib and strip empty/broken repo sections from live env pacman.conf.
# The ISO ships a bare [custom] section with no servers; any repo without a
# Server/Include line causes "no servers configured" for the whole sync.
python3 - <<'PYEOF'
import re, pathlib
p = pathlib.Path('/etc/pacman.conf')
txt = p.read_text()
# Split into [options] block + repo blocks. Keep only repos that have at
# least one Server= or Include= line, or are [core]/[extra] (which always do).
# Then append a clean [multilib] block regardless.
parts = re.split(r'(?=\n\[(?!options))', txt)
kept = []
for part in parts:
    header = re.search(r'^\[([^\]]+)\]', part.lstrip('\n'))
    if not header:
        kept.append(part)
        continue
    name = header.group(1)
    if name in ('options',):
        kept.append(part)
        continue
    # Drop commented-out or empty repo sections (no active Server/Include)
    if not re.search(r'^(?:Server|Include)\s*=', part, re.MULTILINE):
        continue
    # Drop any existing multilib block; we'll re-add a clean one below
    if name.startswith('multilib'):
        continue
    kept.append(part)
txt = ''.join(kept).rstrip('\n') + '\n\n[multilib]\nInclude = /etc/pacman.d/mirrorlist\n'
p.write_text(txt)
PYEOF
pacman -Sy --noconfirm

# Base system + gaming stack
pacstrap /mnt base linux linux-firmware grub efibootmgr sudo \
  networkmanager openssh qemu-guest-agent \
  plasma sddm xdg-desktop-portal-kde \
  steam wine winetricks gamemode lib32-gamemode \
  pipewire pipewire-pulse pipewire-alsa wireplumber \
  %s

# fstab
genfstab -U /mnt >> /mnt/etc/fstab

# Locale + timezone
arch-chroot /mnt ln -sf /usr/share/zoneinfo/UTC /etc/localtime
echo "en_US.UTF-8 UTF-8" >> /mnt/etc/locale.gen
arch-chroot /mnt locale-gen
echo "LANG=en_US.UTF-8" > /mnt/etc/locale.conf

# Hostname
echo "%s" > /mnt/etc/hostname
cat >> /mnt/etc/hosts <<'HOSTSEOF'
127.0.0.1 localhost
::1       localhost
127.0.1.1 %s
HOSTSEOF

# Enable multilib in installed system (same robust approach as live env above)
python3 - <<'PYEOF'
import re, pathlib
p = pathlib.Path('/mnt/etc/pacman.conf')
txt = p.read_text()
parts = re.split(r'(?=\n\[(?!options))', txt)
kept = []
for part in parts:
    header = re.search(r'^\[([^\]]+)\]', part.lstrip('\n'))
    if not header:
        kept.append(part)
        continue
    name = header.group(1)
    if name in ('options',):
        kept.append(part)
        continue
    if not re.search(r'^(?:Server|Include)\s*=', part, re.MULTILINE):
        continue
    if name.startswith('multilib'):
        continue
    kept.append(part)
txt = ''.join(kept).rstrip('\n') + '\n\n[multilib]\nInclude = /etc/pacman.d/mirrorlist\n'
p.write_text(txt)
PYEOF

# Create users
arch-chroot /mnt useradd -m -G wheel,gamemode -s /bin/bash "$USER"
echo "$USER:vee" | arch-chroot /mnt chpasswd
sed -i 's/^# %%wheel ALL=(ALL:ALL) NOPASSWD: ALL/%%wheel ALL=(ALL:ALL) NOPASSWD: ALL/' /mnt/etc/sudoers

# SSH keys
mkdir -p /mnt/home/"$USER"/.ssh
chmod 700 /mnt/home/"$USER"/.ssh
echo "%s" > /mnt/home/"$USER"/.ssh/authorized_keys
chmod 600 /mnt/home/"$USER"/.ssh/authorized_keys
arch-chroot /mnt chown -R "$USER":"$USER" /home/"$USER"/.ssh

# Performance tuning
cat > /mnt/etc/security/limits.d/99-gaming.conf <<'EOF'
* soft nofile 524288
* hard nofile 524288
* soft memlock unlimited
* hard memlock unlimited
* soft rtprio 99
* hard rtprio 99
EOF

cat > /mnt/etc/sysctl.d/99-gaming.conf <<'EOF'
vm.swappiness=10
kernel.split_lock_mitigate=0
vm.nr_hugepages=512
net.core.rmem_max=26214400
net.core.wmem_max=26214400
EOF

# GRUB kernel params
mkdir -p /mnt/etc/default/grub.d
cat > /mnt/etc/default/grub.d/99-gaming.cfg <<'EOF'
GRUB_CMDLINE_LINUX_DEFAULT="$GRUB_CMDLINE_LINUX_DEFAULT split_lock_detect=off"
EOF

# SDDM autologin
mkdir -p /mnt/etc/sddm.conf.d
cat > /mnt/etc/sddm.conf.d/autologin.conf <<EOF
[Autologin]
User=$USER
Session=plasmawayland
EOF

# journal-upload to vee host (gateway resolved at runtime)
cat > /mnt/etc/systemd/journal-upload.conf <<'EOF'
[Upload]
URL=http://vee-host:19532
EOF

# First-boot service to resolve vee-host and finish setup
cat > /mnt/etc/systemd/system/vee-firstboot.service <<'EOF'
[Unit]
Description=vee first-boot finalisation
After=network-online.target
Wants=network-online.target
ConditionPathExists=!/etc/vee-firstboot-done

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/bash /etc/vee-firstboot.sh
ExecStartPost=/bin/touch /etc/vee-firstboot-done

[Install]
WantedBy=multi-user.target
EOF

cat > /mnt/etc/vee-firstboot.sh <<'FBEOF'
#!/bin/bash
GW=$(ip route show default | awk '/default/{print $3; exit}')
if [ -n "$GW" ]; then
  sed -i '/vee-host/d' /etc/hosts
  echo "$GW vee-host" >> /etc/hosts
fi
systemctl enable --now systemd-journal-upload
sysctl --system
FBEOF
chmod +x /mnt/etc/vee-firstboot.sh

%s

arch-chroot /mnt systemctl enable NetworkManager sshd sddm qemu-guest-agent vee-firstboot
arch-chroot /mnt systemctl --global enable pipewire pipewire-pulse wireplumber
%s

# GRUB install
arch-chroot /mnt grub-install --target=x86_64-efi --efi-directory=/boot/efi --bootloader-id=GRUB
arch-chroot /mnt grub-mkconfig -o /boot/grub/grub.cfg

# Set GRUB as the only boot entry — delete PXE/misc entries so the VM
# boots straight to GRUB on next start without waiting for PXE timeouts.
arch-chroot /mnt bash -c '
  grub_num=$(efibootmgr | grep -i "GRUB" | grep -o "Boot[0-9A-F]*" | head -1 | grep -o "[0-9A-F]*")
  if [ -n "$grub_num" ]; then
    efibootmgr --quiet --bootorder "$grub_num"
    # Delete all non-GRUB entries
    efibootmgr | grep "^Boot[0-9A-F]*" | grep -v "Boot'"$grub_num"'" | grep -o "Boot[0-9A-F]*" | while read entry; do
      num="${entry#Boot}"
      efibootmgr --quiet --delete-bootnum --bootnum "$num" || true
    done
  fi
'

%s

cat > /mnt/usr/local/bin/vee-check <<'CHECKEOF'
#!/bin/bash
# Outputs {"checks":[{"name":"...","ok":true,"detail":"..."},...]}
# Exit 0 always; callers inspect JSON for failures.
set -euo pipefail

checks=()

svc_check() {
  local name=$1
  local enabled active
  enabled=$(systemctl is-enabled "$name" 2>&1 || true)
  active=$(systemctl is-active "$name" 2>&1 || true)
  if [[ "$enabled" == "enabled" || "$enabled" == "static" ]] && [[ "$active" == "active" ]]; then
    checks+=("{\"name\":\"svc:$name\",\"ok\":true,\"detail\":\"enabled+active\"}")
  else
    checks+=("{\"name\":\"svc:$name\",\"ok\":false,\"detail\":\"enabled=$enabled active=$active\"}")
  fi
}

# svc_enabled_check asserts the unit is enabled (persists across reboots) but
# does not require it to be active right now. Used for display-manager services
# that cannot start in headless/virtio mode but must still be present for real
# hardware boots.
svc_enabled_check() {
  local name=$1
  local enabled
  enabled=$(systemctl is-enabled "$name" 2>&1 || true)
  if [[ "$enabled" == "enabled" || "$enabled" == "static" ]]; then
    checks+=("{\"name\":\"svc-enabled:$name\",\"ok\":true,\"detail\":\"enabled\"}")
  else
    checks+=("{\"name\":\"svc-enabled:$name\",\"ok\":false,\"detail\":\"enabled=$enabled\"}")
  fi
}

pkg_check() {
  local name=$1
  local out
  out=$(pacman -Q "$name" 2>&1 || true)
  if echo "$out" | grep -q "^$name "; then
    checks+=("{\"name\":\"pkg:$name\",\"ok\":true,\"detail\":\"$(echo "$out" | head -1)\"}")
  else
    checks+=("{\"name\":\"pkg:$name\",\"ok\":false,\"detail\":\"not installed\"}")
  fi
}

file_check() {
  local name=$1 path=$2
  if [[ -e "$path" ]]; then
    checks+=("{\"name\":\"file:$name\",\"ok\":true,\"detail\":\"$path exists\"}")
  else
    checks+=("{\"name\":\"file:$name\",\"ok\":false,\"detail\":\"$path missing\"}")
  fi
}

group_check() {
  local user=$1 group=$2
  if id -nG "$user" 2>/dev/null | grep -qw "$group"; then
    checks+=("{\"name\":\"group:$group\",\"ok\":true,\"detail\":\"$user in $group\"}")
  else
    checks+=("{\"name\":\"group:$group\",\"ok\":false,\"detail\":\"$user not in $group\"}")
  fi
}

# --- Point 1: self-integrity check -------------------------------------------
# Verify this script matches the SHA256 written at install time. A broken
# arch-chroot (wrong arch, missing deps) might still produce a file but with
# truncated or garbage content; this catches that without trusting the installer.
SELF_SHA=$(sha256sum /usr/local/bin/vee-check | awk '{print $1}')
EXPECTED_SHA=$(cat /etc/vee-check.sha256 2>/dev/null || true)
if [[ -n "$EXPECTED_SHA" && "$SELF_SHA" == "$EXPECTED_SHA" ]]; then
  checks+=("{\"name\":\"self-integrity\",\"ok\":true,\"detail\":\"sha256 matches\"}")
elif [[ -z "$EXPECTED_SHA" ]]; then
  checks+=("{\"name\":\"self-integrity\",\"ok\":false,\"detail\":\"no reference checksum at /etc/vee-check.sha256\"}")
else
  checks+=("{\"name\":\"self-integrity\",\"ok\":false,\"detail\":\"sha256 mismatch: got $SELF_SHA want $EXPECTED_SHA\"}")
fi

# --- Point 2: services -------------------------------------------------------
# sddm: assert enabled (survives reboots) but do not require active — headless
# virtio has no display so sddm will not activate on this boot.
svc_check NetworkManager
svc_check sshd
svc_enabled_check sddm
svc_check qemu-guest-agent
# vee-firstboot is oneshot/RemainAfterExit — active means it completed.
svc_check vee-firstboot

# --- Point 4: firstboot actually ran ----------------------------------------
# The service writes this sentinel after ExecStart succeeds. Checking
# is-active alone would pass if RemainAfterExit=yes is set but the script
# never actually ran (e.g. ConditionPathExists suppressed it).
file_check firstboot-done /etc/vee-firstboot-done

# vee-firstboot.sh resolves the host gateway as vee-host; verify the entry
# made it into /etc/hosts (proves the script body executed, not just the unit).
if grep -q 'vee-host' /etc/hosts 2>/dev/null; then
  checks+=("{\"name\":\"firstboot-vee-host\",\"ok\":true,\"detail\":\"vee-host in /etc/hosts\"}")
else
  checks+=("{\"name\":\"firstboot-vee-host\",\"ok\":false,\"detail\":\"vee-host missing from /etc/hosts\"}")
fi

# User/group
group_check gamer wheel
group_check gamer gamemode

# Packages
pkg_check steam
pkg_check plasma-desktop
pkg_check pipewire
pkg_check wireplumber
pkg_check gamemode

# --- Point 3: GPU/SPICE validation ------------------------------------------
# /dev/dri must exist — virtio-gpu creates at least renderD128 at boot.
if [[ -d /dev/dri ]] && ls /dev/dri/renderD* /dev/dri/card* >/dev/null 2>&1; then
  dri_nodes=$(ls /dev/dri/ | tr '\n' ' ')
  checks+=("{\"name\":\"gpu-dri-nodes\",\"ok\":true,\"detail\":\"$dri_nodes\"}")
else
  checks+=("{\"name\":\"gpu-dri-nodes\",\"ok\":false,\"detail\":\"/dev/dri missing or empty\"}")
fi

# SPICE vdagent: the virtio-serial port must be present as a chardev.
# The kernel exposes it as /dev/virtio-ports/com.redhat.spice.0 (symlink to
# the vportNpM device). Its presence proves QEMU wired up the SPICE vdagent
# chardev and the guest kernel loaded the virtio-serial driver.
if [[ -e /dev/virtio-ports/com.redhat.spice.0 ]]; then
  checks+=("{\"name\":\"spice-vdagent-chardev\",\"ok\":true,\"detail\":\"/dev/virtio-ports/com.redhat.spice.0 present\"}")
else
  checks+=("{\"name\":\"spice-vdagent-chardev\",\"ok\":false,\"detail\":\"/dev/virtio-ports/com.redhat.spice.0 missing\"}")
fi

# Config files
file_check sddm-autologin /etc/sddm.conf.d/autologin.conf

# multilib in pacman.conf
if grep -q '\[multilib\]' /etc/pacman.conf 2>/dev/null; then
  checks+=("{\"name\":\"multilib\",\"ok\":true,\"detail\":\"pacman.conf has [multilib]\"}")
else
  checks+=("{\"name\":\"multilib\",\"ok\":false,\"detail\":\"[multilib] missing from pacman.conf\"}")
fi

# sudo nopasswd
if sudo -n true 2>/dev/null; then
  checks+=("{\"name\":\"sudo-nopasswd\",\"ok\":true,\"detail\":\"ok\"}")
else
  checks+=("{\"name\":\"sudo-nopasswd\",\"ok\":false,\"detail\":\"sudo -n true failed\"}")
fi

# fstab >= 2 real entries
fstab_lines=$(grep -vc '^\s*#\|^\s*$' /etc/fstab 2>/dev/null || echo 0)
if [[ "$fstab_lines" -ge 2 ]]; then
  checks+=("{\"name\":\"fstab\",\"ok\":true,\"detail\":\"$fstab_lines entries\"}")
else
  checks+=("{\"name\":\"fstab\",\"ok\":false,\"detail\":\"only $fstab_lines entries\"}")
fi

joined=$(IFS=,; echo "${checks[*]}")
printf '{"checks":[%%s]}\n' "$joined"
CHECKEOF
chmod 0755 /mnt/usr/local/bin/vee-check

# Point 1: write the reference checksum so vee-check can verify itself.
# The chroot is still mounted; compute against the mounted path.
sha256sum /mnt/usr/local/bin/vee-check | awk '{print $1}' > /mnt/etc/vee-check.sha256

umount -R /mnt
poweroff`, user, gpuPkgs, hostname, hostname, strings.Join(sshKeys, "\n"), kasmvncService, nvidiaSetup, kasmvncSetup)

	writeFiles := []vm.CloudInitWriteFile{
		{
			Path:        "/install.sh",
			Content:     installScript,
			Permissions: "0755",
		},
	}
	runCmds := []string{`bash /install.sh`}

	return writeFiles, runCmds
}
