package templates

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestImportMacosvmBundle(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	hw := []byte{0x01, 0x02, 0x03}
	id := []byte{0x04, 0x05}
	manifest := `{
  "version": 1,
  "os": "macos",
  "hardwareModel": "` + base64.StdEncoding.EncodeToString(hw) + `",
  "machineId": "` + base64.StdEncoding.EncodeToString(id) + `",
  "storage": [
    {"type": "disk", "file": "disk.img", "size": 34359738368},
    {"type": "aux", "file": "aux.img"}
  ]
}`
	if err := os.WriteFile(filepath.Join(src, "macosvm.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "disk.img"), []byte("DISK"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "aux.img"), []byte("AUX"), 0o600); err != nil {
		t.Fatal(err)
	}

	diskPath := filepath.Join(dst, "disk.img")
	auxPath := filepath.Join(dst, "aux.img")
	cfg, err := importMacosvmBundle(src, diskPath, auxPath)
	if err != nil {
		t.Fatalf("importMacosvmBundle: %v", err)
	}

	if string(cfg.HardwareModel) != string(hw) || string(cfg.MachineIdentifier) != string(id) {
		t.Errorf("blobs not lifted from manifest: %+v", cfg)
	}
	if cfg.AuxiliaryStorage != auxPath {
		t.Errorf("AuxiliaryStorage = %q, want %q", cfg.AuxiliaryStorage, auxPath)
	}
	for path, want := range map[string]string{diskPath: "DISK", auxPath: "AUX"} {
		data, err := os.ReadFile(path) //nolint:gosec // test temp paths
		if err != nil || string(data) != want {
			t.Errorf("copied %s = (%q, %v), want %q", path, data, err, want)
		}
	}
}

func TestImportMacosvmBundleRejectsIncomplete(t *testing.T) {
	dir := t.TempDir()
	// Missing machineId and aux entry.
	manifest := `{"hardwareModel": "AQI=", "storage": [{"type": "disk", "file": "disk.img"}]}`
	if err := os.WriteFile(filepath.Join(dir, "macosvm.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := importMacosvmBundle(dir, filepath.Join(dir, "d"), filepath.Join(dir, "a")); err == nil {
		t.Error("expected error for manifest missing machineId")
	}
}

func TestCreateSparseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.img")
	if err := createSparseFile(path, 1<<20); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != 1<<20 {
		t.Fatalf("size = %d, want %d (err %v)", info.Size(), 1<<20, err)
	}
	// Growing is fine; shrinking is refused (existing data kept).
	if err := createSparseFile(path, 1<<10); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Size() != 1<<20 {
		t.Errorf("existing larger file was shrunk to %d", info.Size())
	}
}

func TestDefaultGuestPassword(t *testing.T) {
	// macOS refuses a password shorter than 4 characters with
	// eDSAuthPasswordQualityCheckFailed (measured on a macOS 26 guest), so a
	// short account name cannot be its own password.
	tests := map[string]string{
		"vee":      "veevee",
		"ab":       "abab",
		"a":        "aaaa",
		"admin":    "admin",
		"operator": "operator",
	}
	for user, want := range tests {
		got := defaultGuestPassword(user)
		if got != want {
			t.Errorf("defaultGuestPassword(%q) = %q, want %q", user, got, want)
		}
		if len(got) < minGuestPasswordLen {
			t.Errorf("defaultGuestPassword(%q) = %q, shorter than the macOS minimum", user, got)
		}
	}
	if defaultGuestPassword("") != "" {
		t.Error("an empty user should not produce a password")
	}
}
