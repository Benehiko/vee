package provider

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
	flag "github.com/spf13/pflag"
)

type Config struct {
	// StoragePath is the path to the directory where the VMs and ISOs are stored.
	// The default path is "/var/lib/vee".
	StoragePath string `koanf:"storage_path"`
	// VirtiofsdPath is the path to the virtiofsd binary.
	// The default path is "/usr/bin/virtiofsd".
	VirtiofsdPath string `koanf:"virtiofsd_path"`
	// DefaultMemory is the default memory size as a string in the format "size[unit]".
	// For example, "2G" for 2 gigabytes.
	DefaultMemory string `koanf:"default_memory"`
	// DefaultCPUs is the default number of CPUs as a string.
	// For example, "2" for 2 CPUs.
	DefaultCPUs int `koanf:"default_cpus"`
	// DefaultDiskSize is the default disk size as a string in the format "size[unit]".
	// For example, "20G" for 20 gigabytes.
	DefaultDiskSize string `koanf:"default_disk_size"`
	// DefaultCPUModel is the default when creating a new VM.
	// For example, "host", "CascadeLake-Server"
	DefaultCPUModel string `koanf:"default_cpu_model"`
	// DefaultMachineType
	// Default is "q35"
	DefaultMachineType string `koanf:"default_machine_type"`
	// RecreateDisks is a flag to recreate disks when starting a VM.
	RecreateDisks bool `koanf:"recreate_disks"`
}

func newDefaultConfig() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Config{
		StoragePath:        filepath.Join(home, ".config/vee/vms"),
		VirtiofsdPath:      "/usr/bin/virtiofsd",
		DefaultMachineType: "q35",
		DefaultCPUs:        2,
		DefaultMemory:      "2G",
		DefaultDiskSize:    "20G",
		DefaultCPUModel:    "host",
		RecreateDisks:      false,
	}, nil
}

func NewConfig() (*Config, error) {
	k := koanf.New(".")
	y := yaml.Parser()

	config, err := newDefaultConfig()
	if err != nil {
		return nil, err
	}

	if err := k.Load(structs.Provider(config, "koanf"), nil); err != nil {
		return nil, err
	}

	// Use the POSIX compliant pflag lib instead of Go's flag lib.
	f := flag.NewFlagSet("config", flag.ContinueOnError)
	f.Usage = func() {
		fmt.Println(f.FlagUsages())
		os.Exit(0)
	}
	f.Bool("recreate-disks", true, "Recreate disks when starting a VM")
	if err := f.Parse(os.Args[1:]); err != nil {
		log.Fatalf("error parsing flags: %v", err)
	}

	if err := k.Load(posflag.Provider(f, ".", k), nil); err != nil {
		log.Fatalf("error loading config: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	configFilePath := filepath.Join(home, ".config", "vee", "config.yaml")

	_, err = os.Stat(configFilePath)
	if os.IsNotExist(err) {
		// Create the config file if it doesn't exist.
		if err := os.MkdirAll(filepath.Dir(configFilePath), 0755); err != nil {
			return nil, err
		}
		f, err := os.Create(configFilePath)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		if err := os.MkdirAll(config.StoragePath, 0755); err != nil {
			return nil, err
		}
	}

	// Load YAML config and merge into the previously loaded config (because we can).
	err = k.Load(file.Provider(configFilePath), y)
	if err != nil {
		return nil, err
	}

	var c Config
	if err := k.Unmarshal("", &c); err != nil {
		return nil, err
	}

	return &c, nil
}
