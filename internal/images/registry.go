package images

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Benehiko/vee/internal/platform"
	"github.com/Benehiko/vee/provider"
)

// joinWindowsVersions renders a version list for error messages.
func joinWindowsVersions(versions []WindowsVersion) string {
	out := make([]string, len(versions))
	for i, v := range versions {
		out[i] = string(v)
	}
	return strings.Join(out, ", ")
}

const (
	DistroUbuntu  = "ubuntu"
	DistroArch    = "arch"
	DistroFedora  = "fedora"
	DistroTrueNAS = "truenas"
	DistroWindows = "windows"
	DistroAlpine  = "alpine"
	DistroBazzite = "bazzite"
	DistroOmarchy = "omarchy"
)

// SupportedDistros returns the distro slugs usable as GUEST DISTROS for the
// distro-aware templates (devbox/server/desktop/...). DistroMacOS is
// deliberately absent: a macOS IPSW is not a bootable distro image for those
// templates — it is pullable only via PullableDistros / the macos template.
func SupportedDistros() []string {
	return []string{DistroUbuntu, DistroArch, DistroFedora, DistroTrueNAS, DistroWindows, DistroAlpine, DistroBazzite, DistroOmarchy}
}

// PullableDistros returns everything `vee pull` accepts: the guest distros
// plus macOS restore images.
func PullableDistros() []string {
	return append(SupportedDistros(), DistroMacOS)
}

// ImageArch returns the CPU architecture (QEMU naming) of the image vee
// fetches for distro on this host. Ubuntu and Fedora publish multi-arch cloud
// images, Windows media is assembled per host arch from UUP dump, and macOS
// restore images are Apple Silicon by definition — those follow the host.
// Everything else (Arch, Bazzite, TrueNAS, Alpine, Omarchy) publishes
// x86_64-only media; on a non-x86_64 host such a guest only runs under TCG
// emulation, which templates gate behind the --emulate opt-in.
func ImageArch(distro string) string {
	switch distro {
	case DistroUbuntu, DistroFedora, DistroWindows, DistroMacOS:
		return platform.DefaultGuestArch()
	default:
		return "x86_64"
	}
}

// DistroVersions returns the known version strings for a distro, newest first.
func DistroVersions(distro string) []string {
	switch distro {
	case DistroUbuntu:
		out := make([]string, len(KnownUbuntuVersions))
		for i, v := range KnownUbuntuVersions {
			out[i] = string(v)
		}
		return out
	case DistroArch:
		out := make([]string, len(KnownArchVersions))
		for i, v := range KnownArchVersions {
			out[i] = string(v)
		}
		return out
	case DistroFedora:
		out := make([]string, len(KnownFedoraVersions))
		for i, v := range KnownFedoraVersions {
			out[i] = string(v)
		}
		return out
	case DistroTrueNAS:
		out := make([]string, len(KnownTrueNASVersions))
		for i, v := range KnownTrueNASVersions {
			out[i] = string(v)
		}
		return out
	case DistroWindows:
		versions := KnownWindowsVersionsForArch(WindowsHostArch())
		out := make([]string, len(versions))
		for i, v := range versions {
			out[i] = string(v)
		}
		return out
	case DistroAlpine:
		out := make([]string, len(KnownAlpineVersions))
		for i, v := range KnownAlpineVersions {
			out[i] = string(v)
		}
		return out
	case DistroBazzite:
		out := make([]string, len(KnownBazziteVersions))
		for i, v := range KnownBazziteVersions {
			out[i] = string(v)
		}
		return out
	case DistroOmarchy:
		out := make([]string, len(KnownOmarchyVersions))
		for i, v := range KnownOmarchyVersions {
			out[i] = string(v)
		}
		return out
	case DistroMacOS:
		// No pinned list: "latest" is resolved by the host's
		// Virtualization.framework; older versions are pulled by URL.
		return []string{"latest"}
	default:
		return nil
	}
}

// DefaultUser returns the default cloud image username for a distro.
func DefaultUser(distro string) string {
	switch distro {
	case DistroUbuntu:
		return "ubuntu"
	case DistroArch:
		return "arch"
	case DistroFedora:
		return "fedora"
	case DistroAlpine:
		return "alpine"
	default:
		return ""
	}
}

// NewImage constructs the Image for (distro, version).
// version "latest" resolves to the newest known version for the distro.
func NewImage(p provider.Provider, distro, version string) (Image, error) {
	if version == "latest" || version == "" {
		versions := DistroVersions(distro)
		if len(versions) == 0 {
			return nil, fmt.Errorf("unknown distro: %s", distro)
		}
		version = versions[0]
	}

	hostArch := platform.HostArch()

	// macOS restore images are ONLY for Apple Silicon macOS hosts (the vz
	// backend) — there is no emulation path for them.
	if distro == DistroMacOS {
		if !platform.IsMacOS() || hostArch != "arm64" {
			return nil, fmt.Errorf("macos restore images require an Apple Silicon macOS host")
		}
		return NewMacOSImage(p, version)
	}

	// No host-arch gate here: every image is fetchable everywhere. Whether a
	// cross-arch image may BOOT on this host is the templates' concern — they
	// consult ImageArch and require the --emulate opt-in for TCG guests.

	switch distro {
	case DistroUbuntu:
		// Cloud image: pre-installed, cloud-init-ready. Used by devbox/server templates.
		return NewUbuntuCloudImage(p, UbuntuVersion(version), hostArch), nil
	case DistroArch:
		return NewArchImage(p, ArchVersion(version)), nil
	case DistroFedora:
		// Cloud Base qcow2 (cloud-init ready); aarch64 or x86_64 per host.
		return NewFedoraCloudImage(p, FedoraVersion(version), hostArch), nil
	case DistroTrueNAS:
		return NewTrueNASImage(p, TrueNASVersion(version)), nil
	case DistroWindows:
		// Server editions have no public arm64 feature builds on UUP dump, so
		// an arm64 host can only build the client media. Refuse up front with
		// the available set rather than failing deep in the UUP API calls.
		// (This also catches unknown version strings on arm64; amd64 keeps
		// its historical fail-at-the-API behaviour for those.)
		if arch := WindowsHostArch(); arch == "arm64" {
			if !slices.Contains(KnownWindowsVersionsForArch(arch), WindowsVersion(version)) {
				return nil, fmt.Errorf("windows %q is not available for arm64 hosts (UUP dump publishes no arm64 Windows Server feature builds); available on arm64: %s",
					version, joinWindowsVersions(KnownWindowsVersionsForArch(arch)))
			}
		}
		return NewWindowsImage(p, WindowsVersion(version)), nil
	case DistroAlpine:
		return NewAlpineImage(p, AlpineVersion(version)), nil
	case DistroBazzite:
		return NewBazziteImage(p, BazziteVersion(version)), nil
	case DistroOmarchy:
		return NewOmarchyImage(p, OmarchyVersion(version)), nil
	default:
		return nil, fmt.Errorf("unknown distro: %s", distro)
	}
}
