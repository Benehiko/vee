package templates

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/internal/vzfirstboot"
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

// stubPatch replaces the guest-disk patch for the duration of a test.
func stubPatch(t *testing.T, res *vzfirstboot.Result, err error) *int {
	t.Helper()
	calls := 0
	orig := patchGuest
	patchGuest = func(context.Context, string, vzfirstboot.Options) (*vzfirstboot.Result, error) {
		calls++
		return res, err
	}
	t.Cleanup(func() { patchGuest = orig })
	return &calls
}

func TestPatchFirstBootRecordsTheOwedScreenSharingGrant(t *testing.T) {
	// Provisioning enables the guest's Screen Sharing service but cannot
	// authorize it: macOS creates the privacy database those grants live in on
	// the guest's first boot. Unless the config records that vee still owes the
	// grant, no later start ever writes it — which is the bug this exists for.
	stubPatch(t, &vzfirstboot.Result{Password: "veevee"}, nil)
	cfg := &vm.VMConfig{Name: "mac", MacOS: &vm.MacOSConfig{}}
	fbOpts := vzfirstboot.Options{User: "vee", EnableScreenSharing: true}

	if err := patchFirstBoot(t.Context(), cfg, t.TempDir(), "/nonexistent/disk.img", MacOSOptions{}, fbOpts); err != nil {
		t.Fatalf("patchFirstBoot: %v", err)
	}
	if !cfg.MacOS.ScreenSharingGrantPending {
		t.Error("provisioning did not record the owed Screen Sharing grant")
	}
	if cfg.SSHUser != "vee" {
		t.Errorf("ssh user = %q, want %q", cfg.SSHUser, "vee")
	}
}

func TestPatchFirstBootLeavesUnprovisionedGuestsAlone(t *testing.T) {
	// A guest vee did not provision has no Screen Sharing service enabled, so
	// writing privacy grants into it would be an unasked-for change to someone
	// else's installation.
	for _, tc := range []struct {
		name  string
		opts  MacOSOptions
		res   *vzfirstboot.Result
		err   error
		calls int
	}{
		{name: "provisioning skipped", opts: MacOSOptions{SkipFirstBoot: true}, calls: 0},
		{name: "provisioning failed", err: errors.New("unexpected disk layout"), calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubPatch(t, tc.res, tc.err)
			cfg := &vm.VMConfig{Name: "mac", MacOS: &vm.MacOSConfig{}}
			fbOpts := vzfirstboot.Options{User: "vee", EnableScreenSharing: true}

			if err := patchFirstBoot(t.Context(), cfg, t.TempDir(), "/nonexistent/disk.img", tc.opts, fbOpts); err != nil {
				t.Fatalf("patchFirstBoot: %v", err)
			}
			if *calls != tc.calls {
				t.Errorf("patch calls = %d, want %d", *calls, tc.calls)
			}
			if cfg.MacOS.ScreenSharingGrantPending {
				t.Error("recorded an owed Screen Sharing grant for a guest vee never provisioned")
			}
		})
	}
}
