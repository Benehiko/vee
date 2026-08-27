package qemubin

import "github.com/Benehiko/vee/internal/platform"

// PinnedVersion is the vee-qemu release tag to download.
// Update this (and Checksums) when a new QEMU build is published.
//
// Pinned to QEMU 11.1.0: 11.1 brings HVF EL2 (nested virtualization — vee's
// --nested) and Apple's in-kernel vGIC on Apple Silicon hosts, and the macOS
// bundle compiles against the startergo virglrenderer 1.x stack. Note the
// bundle's virgl acceleration has no windowed sink on macOS: upstream
// ui/cocoa is still 2D-only (no dpy_gl ops), so GL boots fall back per the
// manager's GL-less retry.
const PinnedVersion = "qemu-11.1.0-vee1"

// releaseBaseURL is the GitHub Releases download base for vee-qemu assets.
const releaseBaseURL = "https://github.com/Benehiko/vee/releases/download"

// Checksums maps "<os>-<arch>" to the expected SHA-256 of the .tar.gz asset.
// Populated when a release is built. An empty string means no asset is
// published for that platform yet, so Ensure falls back to a system QEMU.
//
// These are the SHA-256 sums of the qemu-11.1.0-vee1 release assets. Each
// bundle ships the QEMU binary plus the edk2/OVMF firmware under share/qemu, so
// with a managed bundle vee needs neither a system QEMU nor an OVMF package.
var Checksums = map[string]string{
	"linux-amd64":   "12fd05f09188581462384c59d4a26192c7617df0813704ef53d62140fc164b30",
	"linux-arm64":   "12fdce014c5ce4f625f606756d530670dbeabc867e0b0356ccd58cf9a53b1583",
	"darwin-arm64":  "fa32e1d09c9a748ad2a598c58df9f66f7e710582c80b4e76c0983c2408a7db79",
	"windows-amd64": "1ef394a66f832c160ad01e588d63080834f42ae274a98dc1398aad08149211ff",
}

// AssetName returns the release asset filename for the given os/arch pair. The
// embedded qemu-system binary name matches the guest architecture native to
// that host arch (qemu-system-aarch64 for arm64, qemu-system-x86_64 for amd64).
func AssetName(goos, goarch string) string {
	binName := platform.QemuBinaryName(platform.GuestArchForHostArch(goarch))
	return binName + "-" + goos + "-" + goarch + ".tar.gz"
}
