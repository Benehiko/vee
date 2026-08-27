package cloudinit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateSeedReadable verifies the installer-seed ISO round-trips with
// the exact file set Omarchy's autoinstall reads. The names must survive
// byte-for-byte (lowercase, underscores, .json extensions): the guest-side
// loader copies them literally off the mounted volume, so a name mangled by
// the ISO layer would silently drop the install back into the wizard.
func TestGenerateSeedReadable(t *testing.T) {
	dir := t.TempDir()

	files := []SeedFile{
		{Name: "user_configuration.json", Content: []byte(`{"hostname": "box"}` + "\n")},
		{Name: "user_credentials.json", Content: []byte(`{"users": []}` + "\n")},
		{Name: "user_full_name.txt", Content: []byte("dev\n")},
		{Name: "user_email_address.txt", Content: []byte("\n")},
		{Name: "user_encrypt_installation.txt", Content: []byte("false\n")},
		{Name: "authorized_keys", Content: []byte("ssh-ed25519 AAAA key\n")},
	}
	isoPath, err := GenerateSeed(dir, files)
	if err != nil {
		t.Fatalf("GenerateSeed: %v", err)
	}
	if filepath.Base(isoPath) != "seed.iso" {
		t.Errorf("seed ISO name = %q, want seed.iso", filepath.Base(isoPath))
	}

	data, err := os.ReadFile(isoPath) //nolint:gosec // isoPath is under the test's own TempDir
	if err != nil {
		t.Fatal(err)
	}
	// Primary Volume Descriptor at sector 16 must carry the cidata label —
	// udev's by-label symlink, which the guest loader waits for, comes from it.
	const pvdOff = 16 * 2048
	volID := strings.TrimRight(string(data[pvdOff+40:pvdOff+72]), " ")
	if volID != "cidata" {
		t.Fatalf("volume ID = %q, want cidata", volID)
	}

	reader := ""
	for _, cand := range []string{"bsdtar", "7z"} {
		if _, err := exec.LookPath(cand); err == nil {
			reader = cand
			break
		}
	}
	if reader == "" {
		t.Skip("no ISO reader (bsdtar/7z) available")
	}

	outDir := t.TempDir()
	var cmd *exec.Cmd
	switch reader {
	case "bsdtar":
		//nolint:gosec // fixed tool name; isoPath/outDir are the test's own TempDir paths
		cmd = exec.CommandContext(context.Background(), "bsdtar", "-x", "-f", isoPath, "-C", outDir)
	case "7z":
		//nolint:gosec // fixed tool name; isoPath/outDir are the test's own TempDir paths
		cmd = exec.CommandContext(context.Background(), "7z", "x", "-y", "-o"+outDir, isoPath)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s extract failed: %v\n%s", reader, err, out)
	}

	for _, f := range files {
		got, err := os.ReadFile(filepath.Join(outDir, f.Name)) //nolint:gosec // outDir is the test's own TempDir
		if err != nil {
			entries, _ := os.ReadDir(outDir)
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Fatalf("%s not extracted under its exact name; found: %s", f.Name, strings.Join(names, ", "))
		}
		if string(got) != string(f.Content) {
			t.Errorf("%s content mismatch:\ngot:  %q\nwant: %q", f.Name, got, f.Content)
		}
	}
}
