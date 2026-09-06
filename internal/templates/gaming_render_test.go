package templates

import (
	"strings"
	"testing"

	"github.com/Benehiko/vee/internal/vm"
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

// kwin's udmabuf import of wl_shm client buffers is fatal on virtio-gpu: the
// host virgl renderer cannot type a guest-memory blob, so the first texture
// sampled from one kills kwin's GL context ("Illegal resource") and the
// compositor freezes. The env file that disables it belongs only to the
// virtio (non-passthrough) install; a passthrough GPU imports udmabuf fine.
func TestGamingDisablesKwinUdmabufOnlyForVirtio(t *testing.T) {
	const conf = "/mnt/etc/environment.d/98-vee-kwin-udmabuf.conf"
	const setting = "KWIN_DISABLE_UDMABUF_IMPORT=1"

	files, _ := archGamingSetup("alano", "s3cr3t", nil, "vm", GamingOptions{
		GPUVendor:   GPUVendorAMD,
		Passthrough: false,
	})
	body := files[0].Content
	if !strings.Contains(body, "cat > "+conf) {
		t.Errorf("virtio install does not write %s", conf)
	}
	if !strings.Contains(body, setting) {
		t.Errorf("virtio install is missing %s", setting)
	}

	files, _ = archGamingSetup("alano", "s3cr3t", nil, "vm", GamingOptions{
		GPUVendor:   GPUVendorAMD,
		Passthrough: true,
		PCIAddr:     "08:00.0",
	})
	if body := files[0].Content; strings.Contains(body, setting) {
		t.Error("passthrough install disables kwin's udmabuf import; a real GPU handles it and the workaround costs upload bandwidth")
	}
}

// Gaming VMs exist to run games, and games need relative mouse deltas for
// mouse-look, so the template's virtio configuration asks for the relative
// pointer by default. Passthrough keeps its own GPU block (real GPU, no
// virtio pointer concern).
func TestGamingDefaultsToRelativePointer(t *testing.T) {
	cfg := gamingGPUConfig(GamingOptions{GPUVendor: GPUVendorAMD})
	if cfg.Mode != vm.GPUVirtio {
		t.Fatalf("gaming GPU mode = %q, want %q", cfg.Mode, vm.GPUVirtio)
	}
	if cfg.Pointer != "mouse" {
		t.Errorf("gaming pointer = %q, want mouse", cfg.Pointer)
	}
}
