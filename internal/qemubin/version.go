package qemubin

import "github.com/Benehiko/vee/internal/platform"

// PinnedVersion is the vee-qemu release tag to download.
// Update this (and Checksums) when a new QEMU build is published.
//
// Pinned to QEMU 11.1.0-rc2: 11.1 brings HVF EL2 (nested virtualization —
// vee's --nested) and Apple's in-kernel vGIC on Apple Silicon hosts, and the
// macOS bundle now compiles against the startergo virglrenderer 1.x stack.
// The rc is deliberate (validated end-to-end on Apple Silicon; upstream final
// is expected ~Aug 2026) — bump to the 11.1.0 final when it ships.
const PinnedVersion = "qemu-11.1.0-rc2-vee1"

// releaseBaseURL is the GitHub Releases download base for vee-qemu assets.
const releaseBaseURL = "https://github.com/Benehiko/vee/releases/download"

// Checksums maps "<os>-<arch>" to the expected SHA-256 of the .tar.gz asset.
// Populated when a release is built. An empty string means no asset is
// published for that platform yet, so Ensure falls back to a system QEMU.
//
// These are the SHA-256 sums of the qemu-11.1.0-rc2-vee1 release assets. Each
// bundle ships the QEMU binary plus the edk2/OVMF firmware under share/qemu, so
// with a managed bundle vee needs neither a system QEMU nor an OVMF package.
var Checksums = map[string]string{
	"linux-amd64":   "9845ee72c1f75008e7f163c0bfc3f422a58fceb2091119d2ed7c2466fca5633f",
	"linux-arm64":   "585c52d7c29d3e7e55918569845b99c049dffa91749b58339447d08de139eb21",
	"darwin-arm64":  "f5ebe7b7cd3dab04c590d9dfa5a53067406f0da89060938afc431a4b51a151fc",
	"windows-amd64": "55b5350fcb0db10f55bb4931d52739d02701947b159ed1d4121bd87019e036ee",
}

// AssetName returns the release asset filename for the given os/arch pair. The
// embedded qemu-system binary name matches the guest architecture native to
// that host arch (qemu-system-aarch64 for arm64, qemu-system-x86_64 for amd64).
func AssetName(goos, goarch string) string {
	binName := platform.QemuBinaryName(platform.GuestArchForHostArch(goarch))
	return binName + "-" + goos + "-" + goarch + ".tar.gz"
}
