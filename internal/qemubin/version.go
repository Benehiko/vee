package qemubin

import "github.com/Benehiko/vee/internal/platform"

// PinnedVersion is the vee-qemu release tag to download.
// Update this (and Checksums) when a new QEMU build is published.
//
// Pinned to a QEMU 10.x base: apple-gfx (ParavirtualizedGraphics.framework for
// macOS guests) landed in QEMU 10.0, and the macOS build carries the
// virglrenderer/ANGLE patch stack for accelerated virtio-gpu.
const PinnedVersion = "qemu-10.0.2-vee1"

// releaseBaseURL is the GitHub Releases download base for vee-qemu assets.
const releaseBaseURL = "https://github.com/Benehiko/vee/releases/download"

// Checksums maps "<os>-<arch>" to the expected SHA-256 of the .tar.gz asset.
// Populated when a release is built. An empty string means no asset is
// published for that platform yet, so Ensure falls back to a system QEMU.
var Checksums = map[string]string{
	"linux-amd64":  "",
	"linux-arm64":  "",
	"darwin-arm64": "",
	// windows-amd64 has no published bundle yet, so Ensure falls back to a
	// system QEMU on PATH (which must be a WHPX-capable build, i.e. QEMU for
	// Windows with --enable-whpx). Populate this when a signed vee-qemu
	// windows-amd64 asset is released.
	"windows-amd64": "",
}

// AssetName returns the release asset filename for the given os/arch pair. The
// embedded qemu-system binary name matches the guest architecture native to
// that host arch (qemu-system-aarch64 for arm64, qemu-system-x86_64 for amd64).
func AssetName(goos, goarch string) string {
	binName := platform.QemuBinaryName(platform.GuestArchForHostArch(goarch))
	return binName + "-" + goos + "-" + goarch + ".tar.gz"
}
