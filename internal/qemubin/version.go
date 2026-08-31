package qemubin

import "github.com/Benehiko/vee/internal/platform"

// PinnedVersion is the vee-qemu release tag to download.
// Update this (and Checksums) when a new QEMU build is published.
//
// Pinned to QEMU 11.1.0: 11.1 brings HVF EL2 (nested virtualization — vee's
// --nested) and Apple's in-kernel vGIC on Apple Silicon hosts, and the macOS
// bundle compiles against the startergo virglrenderer 1.x stack. vee2 adds
// the cocoa OpenGL patch (upstream ui/cocoa is 2D-only), so
// `-display cocoa,gl=es` has a windowed sink and desktop guests render on
// ANGLE/Metal instead of tripping the manager's GL-less retry.
const PinnedVersion = "qemu-11.1.0-vee2"

// releaseBaseURL is the GitHub Releases download base for vee-qemu assets.
const releaseBaseURL = "https://github.com/Benehiko/vee/releases/download"

// Checksums maps "<os>-<arch>" to the expected SHA-256 of the .tar.gz asset.
// Populated when a release is built. An empty string means no asset is
// published for that platform yet, so Ensure falls back to a system QEMU.
//
// These are the SHA-256 sums of the qemu-11.1.0-vee2 release assets. Each
// bundle ships the QEMU binary plus the edk2/OVMF firmware under share/qemu, so
// with a managed bundle vee needs neither a system QEMU nor an OVMF package.
var Checksums = map[string]string{
	"linux-amd64":   "34cff4a37d8891ecf96e0d4414ac645eb7e6dbff9f9b5018de536749d0df4328",
	"linux-arm64":   "0bdde5e2115a90036cd4beb16b82fb5a76ee9be90e7424de8e01cd4b9c930021",
	"darwin-arm64":  "b04638cafe73f37a88207d88abb39238ca72c842d46a9632227b67a1785d58ac",
	"windows-amd64": "fa393f6c60f9d33c226af683979ce55654143ffbeb3be95adef8222b6f3611d7",
}

// AssetName returns the release asset filename for the given os/arch pair. The
// embedded qemu-system binary name matches the guest architecture native to
// that host arch (qemu-system-aarch64 for arm64, qemu-system-x86_64 for amd64).
func AssetName(goos, goarch string) string {
	binName := platform.QemuBinaryName(platform.GuestArchForHostArch(goarch))
	return binName + "-" + goos + "-" + goarch + ".tar.gz"
}
