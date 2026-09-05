package templates

import (
	"strings"
	"testing"
)

func TestGamingInstallScriptRender(t *testing.T) {
	files, runs := archGamingSetup("alano", "s3cr3t", []string{"ssh-ed25519 AAAA..."}, "lotmonster-gaming", GamingOptions{
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
		"reflector --protocol https --latest",
		"pacstrap /mnt base linux",
		"USER=alano",
		"PASSWORD=s3cr3t",
		`echo "$USER:$PASSWORD" | arch-chroot /mnt chpasswd`,
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

// The udev rule that blanks the virtio render node is only correct when a VFIO
// GPU is present for RADV to bind to instead. On a non-passthrough VM the
// virtio node is the only GPU, so hiding it leaves guest Vulkan with no device
// at all — which also takes out the virgl/Venus path.
func TestGamingHideVirtioRenderOnlyForPassthrough(t *testing.T) {
	const rule = "90-vee-hide-virtio-render.rules"

	files, _ := archGamingSetup("alano", "s3cr3t", nil, "vm", GamingOptions{
		GPUVendor:   GPUVendorAMD,
		Passthrough: false,
	})
	if body := files[0].Content; strings.Contains(body, rule) {
		t.Error("non-passthrough install writes the virtio render-node hiding rule; it would blank the only GPU the guest has")
	}

	files, _ = archGamingSetup("alano", "s3cr3t", nil, "vm", GamingOptions{
		GPUVendor:   GPUVendorAMD,
		Passthrough: true,
		PCIAddr:     "08:00.0",
	})
	body := files[0].Content
	if !strings.Contains(body, rule) {
		t.Error("passthrough install is missing the virtio render-node hiding rule")
	}
	if !strings.Contains(body, `SUBSYSTEM=="drm", KERNEL=="renderD*", DRIVERS=="virtio-pci", MODE="0000"`) {
		t.Error("passthrough install is missing the udev match line")
	}
}

// The install script runs from the live ISO with the target root mounted at
// /mnt, so config written to a bare /etc lands on the ISO's tmpfs and is gone
// after the reboot. Every path the installer writes must be under /mnt.
func TestGamingInstallWritesIntoTargetRoot(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		files, _ := archGamingSetup("alano", "s3cr3t", nil, "vm", GamingOptions{
			GPUVendor:   GPUVendorAMD,
			Passthrough: passthrough,
		})
		for _, line := range strings.Split(files[0].Content, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") {
				continue
			}
			for _, prefix := range []string{"cat > /etc/", "mkdir -p /etc/", "cat >> /etc/"} {
				if strings.HasPrefix(line, prefix) {
					t.Errorf("passthrough=%v: install writes to the live ISO instead of the target root: %q",
						passthrough, line)
				}
			}
		}
	}
}
