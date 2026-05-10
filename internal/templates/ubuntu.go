package templates

import (
	"context"
	"path/filepath"
	"time"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/utils"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

// NewUbuntuServerConfig returns a VMConfig for an Ubuntu Server VM, downloading
// the ISO if necessary. The caller can persist and start it via vm.Manager.
func NewUbuntuServerConfig(ctx context.Context, p provider.Provider, version images.UbuntuVersion, name string) (*vm.VMConfig, error) {
	if name == "" {
		name = utils.GeneratePetname()
	}

	img := images.NewUbuntuImage(p, images.UbuntuServer, version, "amd64")
	if err := img.Download(ctx); err != nil {
		return nil, err
	}

	conf := p.Config()
	vmDir := filepath.Join(conf.StoragePath, name)

	cfg := &vm.VMConfig{
		Name:     name,
		Template: "ubuntu-server",
		Memory:   conf.DefaultMemory,
		CPUs:     conf.DefaultCPUs,
		Sockets:  1,
		Cores:    conf.DefaultCPUs,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:  "user",
			Model: "virtio-net-pci",
		},
		GPU: vm.GPUConfig{Mode: vm.GPUNone},
		UEFI: vm.UEFIConfig{
			Enabled:  true,
			CodePath: conf.OVMFCodePath,
		},
		Disks: []vm.DiskConfig{
			{
				Path:       img.AbsolutePath(),
				Format:     "",
				Interface:  "virtio",
				Media:      "cdrom",
				Cache:      "none",
				Readonly:   true,
				InstallISO: true,
			},
			{
				Path:      filepath.Join(vmDir, "storage", "disk-os.qcow2"),
				Size:      conf.DefaultDiskSize,
				Format:    "qcow2",
				Interface: "virtio",
				Media:     "disk",
				Cache:     "writeback",
			},
		},
		CreatedAt: time.Now(),
	}

	return cfg, nil
}

// NewUbuntuServer24Config returns a VMConfig for Ubuntu 24.04 Server.
func NewUbuntuServer24Config(ctx context.Context, p provider.Provider) (*vm.VMConfig, error) {
	return NewUbuntuServerConfig(ctx, p, images.Ubuntu2404, "")
}
