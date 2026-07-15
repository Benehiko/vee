package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/Benehiko/vee/internal/platform"
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
	// DefaultMachineType is host-arch derived: "virt" for aarch64 hosts (Apple
	// Silicon), "q35" for x86_64.
	wantMachine := platform.DefaultMachineType()
	if cfg.DefaultMachineType != wantMachine {
		t.Errorf("DefaultMachineType: got %q, want %q", cfg.DefaultMachineType, wantMachine)
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

func TestLoadConfigAbsolutizesRelativePaths(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	veeDir := filepath.Join(tmp, ".vee")
	if err := os.MkdirAll(veeDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Relative path values must not resolve against the process working
	// directory — a VM's disk would otherwise be written under whatever pwd
	// `vee create` was run from instead of ~/.vee.
	yaml := `storage_path: vms
iso_cache_path: ./iso
log_path: logs
`
	if err := os.WriteFile(filepath.Join(veeDir, "config.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	wantStorage := filepath.Join(veeDir, "vms")
	if cfg.StoragePath != wantStorage {
		t.Errorf("StoragePath: got %q, want %q", cfg.StoragePath, wantStorage)
	}
	wantISO := filepath.Join(veeDir, "iso")
	if cfg.ISOCachePath != wantISO {
		t.Errorf("ISOCachePath: got %q, want %q", cfg.ISOCachePath, wantISO)
	}
	wantLog := filepath.Join(veeDir, "logs")
	if cfg.LogPath != wantLog {
		t.Errorf("LogPath: got %q, want %q", cfg.LogPath, wantLog)
	}
}

func TestLoadConfigKeepsAbsolutePaths(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	veeDir := filepath.Join(tmp, ".vee")
	if err := os.MkdirAll(veeDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	abs := filepath.Join(tmp, "custom-vms")
	yaml := "storage_path: " + abs + "\n"
	if err := os.WriteFile(filepath.Join(veeDir, "config.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.StoragePath != abs {
		t.Errorf("absolute StoragePath should be untouched: got %q, want %q", cfg.StoragePath, abs)
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

func TestFirstExistingPrefersFirstOnDisk(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.fd")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.fd")

	// The first existing candidate wins, even if a later one also exists.
	if got := firstExisting("fallback", missing, present); got != present {
		t.Errorf("firstExisting: got %q, want %q", got, present)
	}
	// When nothing exists, the fallback is returned.
	if got := firstExisting("fallback", missing); got != "fallback" {
		t.Errorf("firstExisting fallback: got %q, want %q", got, "fallback")
	}
}

// defaultFirmware probes several distro OVMF layouts. When none exist on the
// test host it must still return the arch-appropriate fallback rather than an
// empty string, so a VM build produces a clear "firmware not found" copy error
// instead of a confusing empty-path one.
func TestDefaultFirmwareFallsBackToArchDefault(t *testing.T) {
	home := t.TempDir()
	code, vars, secboot := defaultFirmware(home)

	if code == "" || vars == "" || secboot == "" {
		t.Fatalf("defaultFirmware returned an empty path: code=%q vars=%q secboot=%q", code, vars, secboot)
	}

	wantSuffix := "OVMF" // x86_64 fallback
	if platform.DefaultGuestArch() == "aarch64" {
		wantSuffix = "" // aarch64 fallbacks live under ~/.vee or AAVMF; skip the assertion
	}
	if wantSuffix != "" && !strings.Contains(code, wantSuffix) {
		t.Errorf("x86_64 code fallback should reference OVMF: %s", code)
	}
}
