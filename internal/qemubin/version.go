package qemubin

// PinnedVersion is the vee-qemu release tag to download.
// Update this (and Checksums) when a new QEMU build is published.
const PinnedVersion = "qemu-9.2.3-vee1"

// releaseBaseURL is the GitHub Releases download base for vee-qemu assets.
const releaseBaseURL = "https://github.com/Benehiko/vee/releases/download"

// Checksums maps "<os>-<arch>" to the expected SHA-256 of the .tar.gz asset.
// Populated when a release is built. Empty string means "skip checksum" (dev builds).
var Checksums = map[string]string{
	"linux-amd64": "",
	"linux-arm64": "",
}

// AssetName returns the release asset filename for the given os/arch pair.
func AssetName(goos, goarch string) string {
	return "qemu-system-x86_64-" + goos + "-" + goarch + ".tar.gz"
}
