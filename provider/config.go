package provider

import (
	"os"
	"path/filepath"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"

	"github.com/Benehiko/vee/internal/platform"
)

// firstExisting returns the first path in candidates that exists on disk, or
// fallback if none do.
func firstExisting(fallback string, candidates ...string) string {
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return fallback
}

// defaultFirmware returns the UEFI code/vars/secboot firmware paths appropriate
// for the host's native guest architecture. x86_64 guests use OVMF; aarch64
// guests use the edk2 ARM firmware (AAVMF), which on macOS ships with Homebrew
// QEMU, UTM, or the vee-managed bundle. Paths are best-effort defaults; the
// firmware is resolved/installed for real in the qemubin layer.
func defaultFirmware(home string) (code, vars, secboot string) {
	if platform.DefaultGuestArch() == "aarch64" {
		veeCode := filepath.Join(home, ".vee", "share", "qemu", "edk2-aarch64-code.fd")
		veeVars := filepath.Join(home, ".vee", "share", "qemu", "edk2-arm-vars.fd")
		code = firstExisting(veeCode,
			veeCode,
			"/opt/homebrew/share/qemu/edk2-aarch64-code.fd",
			"/usr/local/share/qemu/edk2-aarch64-code.fd",
			"/Applications/UTM.app/Contents/Resources/qemu/edk2-aarch64-code.fd",
			"/usr/share/AAVMF/AAVMF_CODE.fd",
		)
		vars = firstExisting(veeVars,
			veeVars,
			"/opt/homebrew/share/qemu/edk2-arm-vars.fd",
			"/usr/local/share/qemu/edk2-arm-vars.fd",
			"/Applications/UTM.app/Contents/Resources/qemu/edk2-arm-vars.fd",
			"/usr/share/AAVMF/AAVMF_VARS.fd",
		)
		// aarch64 edk2 has no separate Secure Boot code variant; reuse code.
		return code, vars, code
	}
	return "/usr/share/OVMF/x64/OVMF_CODE.4m.fd",
		"/usr/share/OVMF/x64/OVMF_VARS.4m.fd",
		"/usr/share/OVMF/x64/OVMF_CODE.secboot.4m.fd"
}

type Config struct {
	StoragePath         string `koanf:"storage_path"`
	ISOCachePath        string `koanf:"iso_cache_path"`
	VirtiofsdPath       string `koanf:"virtiofsd_path"`
	QemuBinaryPath      string `koanf:"qemu_binary_path"`
	BridgeHelperPath    string `koanf:"bridge_helper_path"`
	OVMFCodePath        string `koanf:"ovmf_code_path"`
	OVMFVarsPath        string `koanf:"ovmf_vars_path"`
	OVMFSecbootCodePath string `koanf:"ovmf_secboot_code_path"`
	LogPath             string `koanf:"log_path"`
	DefaultMemory       string `koanf:"default_memory"`
	DefaultCPUs         int    `koanf:"default_cpus"`
	DefaultDiskSize     string `koanf:"default_disk_size"`
	DefaultCPUModel     string `koanf:"default_cpu_model"`
	DefaultMachineType  string `koanf:"default_machine_type"`
	RecreateDisks       bool   `koanf:"recreate_disks"`

	// MirrorMode selects whether VMs should be wired up to the host-side
	// pacman caching proxy. One of "auto" (use if the unit is active),
	// "on" (force-enable; error if not installed) or "off" (never inject).
	// Default "auto". Set via the global --mirror flag.
	MirrorMode string `koanf:"mirror_mode"`

	// MirrorAddress overrides the guest-side address (host:port) of the
	// host pacoloco proxy. Defaults to "10.0.2.2:9129" which only works in
	// QEMU user-mode networking; bridge-mode VMs need an explicit host IP.
	MirrorAddress string `koanf:"mirror_address"`
}

func newDefaultConfig() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	// VirtiofsdPath is set to the expected install location; the binary is
	// built on demand in buildMachine only when a virtiofs mount is requested.
	virtiofsdPath := filepath.Join(home, ".vee", "bin", "virtiofsd")
	if _, err := os.Stat(virtiofsdPath); err != nil {
		virtiofsdPath = "/usr/bin/virtiofsd"
	}

	// Use the vee-managed QEMU binary when present; fall back to the system one.
	// The binary name is host-arch specific (qemu-system-aarch64 on Apple
	// Silicon, qemu-system-x86_64 on amd64).
	qemuBinName := platform.DefaultQemuBinaryName()
	qemuBinPath := qemuBinName
	veeManagedQemu := filepath.Join(home, ".vee", "bin", qemuBinName)
	if _, err := os.Stat(veeManagedQemu); err == nil {
		qemuBinPath = veeManagedQemu
	}

	// QEMU bridge networking via the setuid qemu-bridge-helper is Linux-only;
	// macOS uses user-mode NAT. Probe common locations on Linux, leave empty
	// elsewhere.
	bridgeHelper := ""
	if platform.SupportsBridgeNetworking() {
		bridgeHelper = firstExisting("/usr/lib/qemu/qemu-bridge-helper",
			"/usr/lib/qemu/qemu-bridge-helper",
			"/usr/lib/qemu-kvm/qemu-bridge-helper",
			"/usr/libexec/qemu-bridge-helper",
		)
	}

	ovmfCode, ovmfVars, ovmfSecboot := defaultFirmware(home)

	return &Config{
		StoragePath:         filepath.Join(home, ".vee/vms"),
		ISOCachePath:        filepath.Join(home, ".vee/iso"),
		VirtiofsdPath:       virtiofsdPath,
		QemuBinaryPath:      qemuBinPath,
		BridgeHelperPath:    bridgeHelper,
		OVMFCodePath:        ovmfCode,
		OVMFVarsPath:        ovmfVars,
		OVMFSecbootCodePath: ovmfSecboot,
		LogPath:             filepath.Join(home, ".vee", "logs"),
		DefaultMachineType:  platform.DefaultMachineType(),
		DefaultCPUs:         2,
		DefaultMemory:       "2G",
		DefaultDiskSize:     "20G",
		DefaultCPUModel:     "host",
		RecreateDisks:       false,
		MirrorMode:          "auto",
		MirrorAddress:       "10.0.2.2:9129",
	}, nil
}

func NewConfig() (*Config, error) {
	defaults, err := newDefaultConfig()
	if err != nil {
		return nil, err
	}
	return loadConfig(defaults)
}

func loadConfig(defaults *Config) (*Config, error) {
	k := koanf.New(".")
	y := yaml.Parser()

	if err := k.Load(structs.Provider(defaults, "koanf"), nil); err != nil {
		return nil, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	configFilePath := filepath.Join(home, ".vee", "config.yaml")

	if _, err := os.Stat(configFilePath); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(configFilePath), 0o750); err != nil {
			return nil, err
		}
		//nolint:gosec // configFilePath is derived from the user's home dir, not external input.
		f, err := os.Create(configFilePath)
		if err != nil {
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}

		if err := os.MkdirAll(defaults.StoragePath, 0o750); err != nil {
			return nil, err
		}
	}

	if err := k.Load(file.Provider(configFilePath), y); err != nil {
		return nil, err
	}

	var c Config
	if err := k.Unmarshal("", &c); err != nil {
		return nil, err
	}
	return &c, nil
}
