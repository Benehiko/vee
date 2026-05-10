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

// DataDisk represents a host block device to pass through as a ZFS data drive.
// Serial is optional; if empty it is derived from the disk-by-id path.
type DataDisk struct {
	Path   string
	Serial string
}

// ParseDataDisk parses a "path" or "path:serial" string into a DataDisk.
func ParseDataDisk(s string) DataDisk {
	if idx := strings.LastIndex(s, ":"); idx > 0 {
		return DataDisk{Path: s[:idx], Serial: s[idx+1:]}
	}
	return DataDisk{Path: s}
}

// NewTruenasConfig returns a VMConfig for a TrueNAS SCALE VM.
//
// version selects the TrueNAS SCALE ISO version ("latest" for newest).
// bridge is the host bridge interface for LAN access to the web UI (default "br0").
// spicePort is the local SPICE display port (default 5933).
// dataDisks are host block devices to pass through as ZFS data drives.
// Each entry is "path" or "path:serial"; serial defaults to auto-derived from path.
//
// TrueNAS requires UEFI, bridge networking (to reach the web UI at port 80/443
// on the VM's LAN IP), and SATA/AHCI for its OS boot pool disks. Data drives
// are passed through as virtio-blk-pci with serials derived from the disk-by-id
// name so ZFS can identify physical drives after reboots.
func NewTruenasConfig(ctx context.Context, p provider.Provider, name, version string, bridge string, spicePort int, dataDisks []string) (*vm.VMConfig, error) {
	if version == "" {
		version = "latest"
	}
	if bridge == "" {
		bridge = "br0"
	}
	// port 0 → manager assigns a random free port at create time
	_ = spicePort
	spicePort = 0

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
			Path:       img.AbsolutePath(),
			Format:     "raw",
			Interface:  "none",
			Media:      "cdrom",
			Cache:      "none",
			Readonly:   true,
			InstallISO: true,
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

	for _, raw := range dataDisks {
		dd := ParseDataDisk(raw)
		serial := dd.Serial
		if serial == "" {
			serial = truenasSerialFromPath(dd.Path)
		}
		disks = append(disks, vm.DiskConfig{
			Path:        dd.Path,
			Format:      "raw",
			Interface:   "virtio",
			Media:       "disk",
			Cache:       "none",
			Passthrough: true,
			Serial:      serial,
		})
	}

	return &vm.VMConfig{
		Name:     name,
		Template: "truenas",
		Memory:   "4G",
		CPUs:     1,
		Sockets:  1,
		Cores:    1,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:   "bridge",
			Bridge: bridge,
			Model:  "virtio-net-pci",
		},
		GPU:        vm.GPUConfig{Mode: vm.GPUNone},
		Headless:   false,
		GuestAgent: true,
		UEFI: vm.UEFIConfig{
			Enabled: true,
		},
		SPICE: &vm.SPICEConfig{
			Port:             spicePort,
			DisableTicketing: true,
		},
		Services: []vm.ServiceEntry{
			{Name: "spice", Port: 0, Protocol: vm.ServiceSPICE},
			{Name: "truenas-ui", Port: 443, Protocol: vm.ServiceHTTPS},
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
