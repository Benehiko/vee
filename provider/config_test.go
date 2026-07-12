package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestNewDefaultConfigDefaults(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg, err := newDefaultConfig()
	if err != nil {
		t.Fatalf("newDefaultConfig: %v", err)
	}

	if !strings.HasPrefix(cfg.StoragePath, tmp) {
		t.Errorf("StoragePath should be under HOME: %s", cfg.StoragePath)
	}
	if cfg.DefaultMemory != "2G" {
		t.Errorf("DefaultMemory: got %q, want 2G", cfg.DefaultMemory)
	}
	if cfg.DefaultCPUs != 2 {
		t.Errorf("DefaultCPUs: got %d, want 2", cfg.DefaultCPUs)
	}
	if cfg.DefaultDiskSize != "20G" {
		t.Errorf("DefaultDiskSize: got %q, want 20G", cfg.DefaultDiskSize)
	}
	if cfg.DefaultCPUModel != "host" {
		t.Errorf("DefaultCPUModel: got %q, want host", cfg.DefaultCPUModel)
	}
	if cfg.DefaultMachineType != "q35" {
		t.Errorf("DefaultMachineType: got %q, want q35", cfg.DefaultMachineType)
	}
	if cfg.RecreateDisks {
		t.Error("RecreateDisks should default to false")
	}
}

func TestNewConfigCreatesVeeDirs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmp, ".vee")); err != nil {
		t.Errorf(".vee dir not created: %v", err)
	}
	if !strings.Contains(cfg.StoragePath, ".vee") {
		t.Errorf("StoragePath should be inside .vee: %s", cfg.StoragePath)
	}
}

func TestLoadConfigFromYAML(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	veeDir := filepath.Join(tmp, ".vee")
	if err := os.MkdirAll(veeDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yaml := `default_memory: 8G
default_cpus: 4
`
	if err := os.WriteFile(filepath.Join(veeDir, "config.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	if cfg.DefaultMemory != "8G" {
		t.Errorf("DefaultMemory: got %q, want 8G", cfg.DefaultMemory)
	}
	if cfg.DefaultCPUs != 4 {
		t.Errorf("DefaultCPUs: got %d, want 4", cfg.DefaultCPUs)
	}
	// Fields not in YAML keep defaults.
	if cfg.DefaultDiskSize != "20G" {
		t.Errorf("DefaultDiskSize should remain default 20G: got %q", cfg.DefaultDiskSize)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// NewConfig creates the file if missing — should not error.
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig with no pre-existing config file: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
}
