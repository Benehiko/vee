package provider

import (
	"os"
	"path/filepath"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	StoragePath        string `koanf:"storage_path"`
	VirtiofsdPath      string `koanf:"virtiofsd_path"`
	QemuBinaryPath     string `koanf:"qemu_binary_path"`
	OVMFCodePath       string `koanf:"ovmf_code_path"`
	OVMFVarsPath       string `koanf:"ovmf_vars_path"`
	LogPath            string `koanf:"log_path"`
	DefaultMemory      string `koanf:"default_memory"`
	DefaultCPUs        int    `koanf:"default_cpus"`
	DefaultDiskSize    string `koanf:"default_disk_size"`
	DefaultCPUModel    string `koanf:"default_cpu_model"`
	DefaultMachineType string `koanf:"default_machine_type"`
	RecreateDisks      bool   `koanf:"recreate_disks"`
}

func newDefaultConfig() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Config{
		StoragePath:        filepath.Join(home, ".config/vee/vms"),
		VirtiofsdPath:      "/usr/bin/virtiofsd",
		QemuBinaryPath:     "qemu-system-x86_64",
		OVMFCodePath:       "/usr/share/OVMF/x64/OVMF_CODE.4m.fd",
		OVMFVarsPath:       "/usr/share/OVMF/x64/OVMF_VARS.4m.fd",
		LogPath:            filepath.Join(home, ".float/state/logs"),
		DefaultMachineType: "q35",
		DefaultCPUs:        2,
		DefaultMemory:      "2G",
		DefaultDiskSize:    "20G",
		DefaultCPUModel:    "host",
		RecreateDisks:      false,
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
	configFilePath := filepath.Join(home, ".config", "vee", "config.yaml")

	if _, err := os.Stat(configFilePath); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(configFilePath), 0o755); err != nil {
			return nil, err
		}
		f, err := os.Create(configFilePath)
		if err != nil {
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}

		if err := os.MkdirAll(defaults.StoragePath, 0o755); err != nil {
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
