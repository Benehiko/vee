package cloudinit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildISONativeReadable verifies the pure-Go ISO writer produces an image
// that a standard ISO reader can list and whose file contents round-trip. This
// is the writer used on Windows (and as a no-external-tool fallback), so its
// correctness cannot rely on xorriso/genisoimage/hdiutil.
func TestBuildISONativeReadable(t *testing.T) {
	dir := t.TempDir()
	udPath := filepath.Join(dir, "user-data")
	mdPath := filepath.Join(dir, "meta-data")
	isoPath := filepath.Join(dir, "cidata.iso")

	udContent := "#cloud-config\nhostname: veetest\n"
	mdContent := "instance-id: veetest\nlocal-hostname: veetest\n"
	if err := os.WriteFile(udPath, []byte(udContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mdPath, []byte(mdContent), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := buildISONative(isoPath, udPath, mdPath); err != nil {
		t.Fatalf("buildISONative: %v", err)
	}

	data, err := os.ReadFile(isoPath) //nolint:gosec // isoPath is under the test's own TempDir
	if err != nil {
		t.Fatal(err)
	}
	// Primary Volume Descriptor signature at sector 16.
	const magicOff = 16*2048 + 1
	if string(data[magicOff:magicOff+5]) != "CD001" {
		t.Fatalf("missing CD001 at %d", magicOff)
	}
	// Joliet Supplementary Volume Descriptor at sector 17 (type 2 + CD001).
	const jolietOff = 17 * 2048
	if data[jolietOff] != 2 || string(data[jolietOff+1:jolietOff+6]) != "CD001" {
		t.Fatalf("missing Joliet SVD at sector 17")
	}

	// Use bsdtar or 7z (no mount privileges needed) to list and extract. Skip if
	// neither is present so the test still passes on a bare runner; the byte-level
	// checks above already gate structural correctness.
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

	got, err := os.ReadFile(filepath.Join(outDir, "user-data")) //nolint:gosec // outDir is the test's own TempDir
	if err != nil {
		// Joliet may preserve exact case; some extractors expose the primary
		// (upper) name. Accept either.
		got, err = os.ReadFile(filepath.Join(outDir, "USER-DATA")) //nolint:gosec // outDir is the test's own TempDir
		if err != nil {
			entries, _ := os.ReadDir(outDir)
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Fatalf("user-data not extracted; found: %s", strings.Join(names, ", "))
		}
	}
	if strings.TrimSpace(string(got)) != strings.TrimSpace(udContent) {
		t.Fatalf("user-data content mismatch:\ngot:  %q\nwant: %q", got, udContent)
	}
}
