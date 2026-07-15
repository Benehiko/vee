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
//
// These are the SHA-256 sums of the qemu-10.0.2-vee1 release assets. Each bundle
// ships the QEMU binary plus the edk2/OVMF firmware under share/qemu, so with a
// managed bundle vee needs neither a system QEMU nor an OVMF package.
var Checksums = map[string]string{
	"linux-amd64":   "6230cead3993626fde6ebf94570fecc532e30c6b7f316810fc339ea1e8ee9b26",
	"linux-arm64":   "fc685badfb0a5983abe2c34dc3eafe6c574dde74ff2f2f61ae22c8df342f25f5",
	"darwin-arm64":  "f651b58be4d9ba2fb351639d45b4fb8a9af493869ebbefa584722cf70bba0690",
	"windows-amd64": "8dbf64c69fd4147ad074faa3f43f8991870fa38db244e747655e38dc492f7779",
}

// AssetName returns the release asset filename for the given os/arch pair. The
// embedded qemu-system binary name matches the guest architecture native to
// that host arch (qemu-system-aarch64 for arm64, qemu-system-x86_64 for amd64).
func AssetName(goos, goarch string) string {
	binName := platform.QemuBinaryName(platform.GuestArchForHostArch(goarch))
	return binName + "-" + goos + "-" + goarch + ".tar.gz"
}
