package templates

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Benehiko/vee/images"
	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/vm"
)

// NewTruenasConfig returns a VMConfig for a TrueNAS SCALE VM.
//
// version selects the TrueNAS SCALE ISO version ("latest" for newest).
// bridge is the host bridge interface for LAN access to the web UI (default "br0").
// spicePort is the local SPICE display port (default 5933).
// dataDiskPaths are host block device paths to pass through as ZFS data drives
// (e.g. /dev/disk/by-id/ata-ST22000NM000C_ZXA0S3H6). Leave nil for no data disks.
//
// TrueNAS requires UEFI, bridge networking (to reach the web UI at port 80/443
// on the VM's LAN IP), and SATA/AHCI for its OS boot pool disks. Data drives
// are passed through as virtio-blk-pci with serials derived from the disk-by-id
// name so ZFS can identify physical drives after reboots.
func NewTruenasConfig(ctx context.Context, p provider.Provider, name, version string, bridge string, spicePort int, dataDiskPaths []string) (*vm.VMConfig, error) {
	if version == "" {
		version = "latest"
	}
	if bridge == "" {
		bridge = "br0"
	}
	if spicePort == 0 {
		spicePort = 5933
	}

	img, err := images.NewImage(p, images.DistroTrueNAS, version)
	if err != nil {
		return nil, fmt.Errorf("truenas image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("truenas image download: %w", err)
	}

	conf := p.Config()
	vmDir := filepath.Join(conf.StoragePath, name)

	disks := []vm.DiskConfig{
		{
			// TrueNAS installer ISO — booted via USB storage device emulation.
			Path:     img.AbsolutePath(),
			Format:   "raw",
			Interface: "none",
			Media:    "cdrom",
			Cache:    "none",
			Readonly: true,
		},
		{
			// Primary OS disk on AHCI/SATA for ZFS boot pool.
			Path:      filepath.Join(vmDir, "storage", "disk-os.qcow2"),
			Size:      "16G",
			Format:    "qcow2",
			Interface: "ahci",
			Media:     "disk",
			Cache:     "none",
		},
	}

	for _, devPath := range dataDiskPaths {
		disks = append(disks, vm.DiskConfig{
			Path:        devPath,
			Format:      "raw",
			Interface:   "virtio",
			Media:       "disk",
			Cache:       "none",
			Passthrough: true,
			Serial:      truenasSerialFromPath(devPath),
		})
	}

	return &vm.VMConfig{
		Name:     name,
		Template: "truenas",
		Memory:   "8G",
		CPUs:     2,
		Sockets:  1,
		Cores:    2,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:   "bridge",
			Bridge: bridge,
			Model:  "virtio-net-pci",
		},
		GPU:      vm.GPUConfig{Mode: vm.GPUNone},
		Headless: false,
		UEFI: vm.UEFIConfig{
			Enabled: true,
		},
		SPICE: &vm.SPICEConfig{
			Port:             spicePort,
			DisableTicketing: true,
		},
		Disks:     disks,
		CreatedAt: time.Now(),
	}, nil
}

// truenasSerialFromPath derives a QEMU disk serial from a /dev/disk/by-id path.
// Strips well-known prefixes (ata-, scsi-, nvme-, wwn-) and truncates to 20 chars
// (QEMU's serial field limit) so ZFS sees a stable, meaningful identifier.
func truenasSerialFromPath(devPath string) string {
	base := filepath.Base(devPath)
	for _, prefix := range []string{"ata-", "scsi-", "nvme-", "wwn-", "usb-"} {
		base = strings.TrimPrefix(base, prefix)
	}
	// Remove partition suffixes like -part1
	if idx := strings.LastIndex(base, "-part"); idx > 0 {
		base = base[:idx]
	}
	if len(base) > 20 {
		base = base[:20]
	}
	return base
}
