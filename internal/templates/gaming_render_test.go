package templates

import (
	"strings"
	"testing"
)

func TestGamingInstallScriptRender(t *testing.T) {
	files, runs := archGamingSetup("alano", []string{"ssh-ed25519 AAAA..."}, "lotmonster-gaming", GamingOptions{
		GPUVendor:   GPUVendorAMD,
		Passthrough: false,
	})
	if len(files) == 0 {
		t.Fatal("no write_files generated")
	}
	if len(runs) == 0 {
		t.Fatal("no runcmd generated")
	}
	body := files[0].Content
	for _, want := range []string{
		"set -euxo pipefail",
		"timedatectl set-ntp true",
		"trap 'on_err $LINENO' ERR",
		"reflector --latest",
		"pacstrap /mnt base linux",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("install script missing %q", want)
		}
	}
	for _, bad := range []string{"%%", "%!", "%(MISSING)"} {
		if strings.Contains(body, bad) {
			t.Errorf("install script contains stray %q (Sprintf escape leaked)", bad)
		}
	}
}
