package templates

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/shacrypt"
	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

// Omarchy install target disk. The ISO alone is >6G and a stock Omarchy
// install lands tens of GB of packages, so the 20G provider default is too
// small to be a useful desktop.
const omarchyDiskBytes = 60 << 30 // 60 GiB

// OmarchyOptions holds optional overrides for Omarchy VM creation.
type OmarchyOptions struct {
	// Template records which template requested the build ("omarchy" when
	// empty) — the desktop/devbox templates delegate here for --distro omarchy.
	Template string
	// User is the guest login username (default "omarchy").
	User string
	// Password is the guest login password (defaults to the username, like the
	// gaming templates). Seeded into the installer as a SHA-512 crypt hash.
	Password string
}

// NewOmarchyConfig builds a VMConfig for an Omarchy desktop VM.
//
// Omarchy (https://omarchy.org/) is an opinionated Arch + Hyprland desktop.
// The install is fully unattended: alongside the install ISO vee attaches a
// second, cidata-labelled seed ISO carrying the answers Omarchy's own wizard
// would have written (archinstall user_configuration.json + credentials +
// authorized_keys — the same mechanism Omarchy's manual documents for
// imaging rigs and VM fleets). The installer partitions the target disk,
// installs, and reboots into the finished system on its own; the seeded
// authorized_keys make it enable sshd, so `vee ssh` works from first boot.
//
// version selects the ISO release ("latest" for newest). Hyprland is a
// Wayland compositor and needs a GL-capable GPU, so the guest gets
// virtio-gpu in GL mode (virgl). Both ISOs are flagged InstallISO: vee
// ejects them once the installed system boots from disk.
func NewOmarchyConfig(ctx context.Context, p provider.Provider, name string, sshKeys []string, version string, opts OmarchyOptions) (*vm.VMConfig, error) {
	if version == "" {
		version = "latest"
	}
	template := opts.Template
	if template == "" {
		template = "omarchy"
	}
	user := opts.User
	if user == "" {
		user = "omarchy"
	}
	password := opts.Password
	if password == "" {
		password = user
	}

	img, err := images.NewImage(p, images.DistroOmarchy, version)
	if err != nil {
		return nil, fmt.Errorf("omarchy image: %w", err)
	}
	if err := img.Download(ctx); err != nil {
		return nil, fmt.Errorf("omarchy image download: %w", err)
	}

	seedFiles, err := omarchySeedFiles(name, user, password, sshKeys)
	if err != nil {
		return nil, fmt.Errorf("omarchy seed: %w", err)
	}

	conf := p.Config()
	vmDir := filepath.Join(conf.StoragePath, name)

	return &vm.VMConfig{
		Name:     name,
		Template: template,
		Memory:   "8G",
		CPUs:     4,
		Sockets:  1,
		Cores:    4,
		Threads:  1,
		CPUModel: conf.DefaultCPUModel,
		NIC: vm.NICConfig{
			Mode:  "user",
			Model: "virtio-net-pci",
		},
		// Accelerated virtio-gpu (virgl) — Hyprland needs GL.
		GPU:      vm.GPUConfig{Mode: vm.GPUVirtio},
		Headless: false,
		SSHPort:  deterministicSSHPort(name),
		UEFI:     vm.UEFIConfig{Enabled: true},
		Disks: []vm.DiskConfig{
			{
				// Install target disk — the seeded config points the installer here.
				Path:      filepath.Join(vmDir, "storage", "disk-os.qcow2"),
				Size:      fmt.Sprintf("%dG", omarchyDiskBytes>>30),
				Format:    "qcow2",
				Interface: "virtio",
				Media:     "disk",
				Cache:     "writeback",
			},
			{
				// Boot from the Omarchy ISO for first-time install.
				Path:       img.AbsolutePath(),
				Format:     "raw",
				Interface:  "ide",
				Media:      "cdrom",
				Readonly:   true,
				InstallISO: true,
			},
		},
		SeedFiles:  seedFiles,
		SSHUser:    user,
		GuestAgent: true,
		CreatedAt:  time.Now(),
	}, nil
}

// omarchySeedFiles renders the autoinstall answers Omarchy's cidata loader
// expects: the same files its interactive configurator writes. The layout
// mirrors the wizard's own full-disk plan (and Omarchy's integration tests):
// a 1MiB gap, a 2GiB fat32 ESP at /boot, and the rest btrfs with the
// @/@home/@log/@pkg subvolumes, on the first virtio disk.
func omarchySeedFiles(hostname, user, password string, sshKeys []string) ([]vm.SeedFile, error) {
	hash, err := shacrypt.GenerateHash(password)
	if err != nil {
		return nil, err
	}

	credentials := map[string]any{
		"root_enc_password": hash,
		"users": []map[string]any{
			{
				"enc_password": hash,
				"groups":       []string{},
				"sudo":         true,
				"username":     user,
			},
		},
	}
	credentialsJSON, err := json.MarshalIndent(credentials, "", "    ")
	if err != nil {
		return nil, err
	}

	configJSON, err := json.MarshalIndent(omarchyInstallConfig(hostname), "", "    ")
	if err != nil {
		return nil, err
	}

	return []vm.SeedFile{
		{Name: "user_configuration.json", Content: string(configJSON) + "\n"},
		{Name: "user_credentials.json", Content: string(credentialsJSON) + "\n"},
		// The wizard always writes the git-identity files, so downstream code
		// may assume they exist; a deferred-provisioning install writes them
		// empty, which is the precedent for values we cannot know.
		{Name: "user_full_name.txt", Content: user + "\n"},
		{Name: "user_email_address.txt", Content: "\n"},
		{Name: "user_encrypt_installation.txt", Content: "false\n"},
		{Name: "authorized_keys", Content: strings.Join(sshKeys, "\n") + "\n"},
	}, nil
}

// omarchyInstallConfig builds the archinstall user_configuration.json for an
// unattended full-disk install onto /dev/vda. Field-for-field this follows
// what Omarchy's configurator writes (and its integration harness seeds),
// with the disk layout expressed declaratively so the orchestrator performs
// the partitioning itself.
func omarchyInstallConfig(hostname string) map[string]any {
	const (
		mib = int64(1) << 20
		gib = int64(1) << 30
	)
	bootStart := mib
	bootSize := 2 * gib
	mainStart := bootStart + bootSize
	// Leave 1MiB at the tail for the GPT backup, like the wizard does.
	mainSize := omarchyDiskBytes - mainStart - mib

	sizeValue := func(v int64) map[string]any {
		return map[string]any{
			"sector_size": map[string]any{"unit": "B", "value": 512},
			"unit":        "B",
			"value":       v,
		}
	}

	return map[string]any{
		"app_config":           nil,
		"archinstall-language": "English",
		"auth_config":          map[string]any{},
		"audio_config":         map[string]any{"audio": "pipewire"},
		"bootloader_config":    map[string]any{"bootloader": "Limine", "uki": false, "removable": false},
		"custom_commands":      []any{},
		"omarchy_install": map[string]any{
			"mode":               "full_disk",
			"defer_provisioning": false,
			"target_mount":       "/mnt",
			"boot": map[string]any{
				"esp_mount":       "/boot",
				"esp_path":        "/EFI/limine",
				"efi_binary":      "limine_x64.efi",
				"enable_fallback": true,
			},
			"storage": map[string]any{"kernel": "linux"},
		},
		"disk_config": map[string]any{
			"config_type": "default_layout",
			"device_modifications": []map[string]any{
				{
					"device": "/dev/vda",
					"wipe":   true,
					"partitions": []map[string]any{
						{
							"btrfs":         []any{},
							"dev_path":      nil,
							"flags":         []string{"boot", "esp"},
							"fs_type":       "fat32",
							"mount_options": []any{},
							"mountpoint":    "/boot",
							"obj_id":        "ea21d3f2-82bb-49cc-ab5d-6f81ae94e18d",
							"size":          sizeValue(bootSize),
							"start":         sizeValue(bootStart),
							"status":        "create",
							"type":          "primary",
						},
						{
							"btrfs": []map[string]any{
								{"mountpoint": "/", "name": "@"},
								{"mountpoint": "/home", "name": "@home"},
								{"mountpoint": "/var/log", "name": "@log"},
								{"mountpoint": "/var/cache/pacman/pkg", "name": "@pkg"},
							},
							"dev_path":      nil,
							"flags":         []any{},
							"fs_type":       "btrfs",
							"mount_options": []string{"compress=zstd"},
							"mountpoint":    nil,
							"obj_id":        "8c2c2b92-1070-455d-b76a-56263bab24aa",
							"size":          sizeValue(mainSize),
							"start":         sizeValue(mainStart),
							"status":        "create",
							"type":          "primary",
						},
					},
				},
			},
		},
		"hostname":           hostname,
		"kernels":            []string{"linux"},
		"network_config":     map[string]any{"type": "iso"},
		"ntp":                true,
		"parallel_downloads": 8,
		"script":             nil,
		"services":           []any{},
		"swap":               true,
		"timezone":           "UTC",
		"locale_config": map[string]any{
			"kb_layout": "us",
			"sys_enc":   "UTF-8",
			"sys_lang":  "en_US.UTF-8",
		},
		"mirror_config": map[string]any{
			"custom_repositories": []any{},
			"custom_servers": []map[string]any{
				{"url": "https://mirror.omarchy.org/$repo/os/$arch"},
				{"url": "https://mirror.rackspace.com/archlinux/$repo/os/$arch"},
				{"url": "https://geo.mirror.pkgbuild.com/$repo/os/$arch"},
			},
			"mirror_regions":        map[string]any{},
			"optional_repositories": []any{},
		},
		"packages": []string{
			"base-devel",
			"git",
			"omarchy-keyring",
			"omarchy-settings",
			"omarchy",
		},
		"profile_config": map[string]any{"gfx_driver": nil, "greeter": nil, "profile": map[string]any{}},
		"version":        "3.0.9",
	}
}
