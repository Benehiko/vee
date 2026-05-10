package images

import (
	"fmt"

	"github.com/Benehiko/vee/provider"
)

const (
	DistroUbuntu  = "ubuntu"
	DistroArch    = "arch"
	DistroFedora  = "fedora"
	DistroTrueNAS = "truenas"
	DistroWindows = "windows"
	DistroAlpine  = "alpine"
	DistroBazzite = "bazzite"
)

// SupportedDistros returns all known distro slugs.
func SupportedDistros() []string {
	return []string{DistroUbuntu, DistroArch, DistroFedora, DistroTrueNAS, DistroWindows, DistroAlpine, DistroBazzite}
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
		out := make([]string, len(KnownWindowsVersions))
		for i, v := range KnownWindowsVersions {
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

	switch distro {
	case DistroUbuntu:
		// Cloud image: pre-installed, cloud-init-ready. Used by devbox/server templates.
		return NewUbuntuCloudImage(p, UbuntuVersion(version)), nil
	case DistroArch:
		return NewArchImage(p, ArchVersion(version)), nil
	case DistroFedora:
		return NewFedoraImage(p, FedoraVersion(version)), nil
	case DistroTrueNAS:
		return NewTrueNASImage(p, TrueNASVersion(version)), nil
	case DistroWindows:
		return NewWindowsImage(p, WindowsVersion(version)), nil
	case DistroAlpine:
		return NewAlpineImage(p, AlpineVersion(version)), nil
	case DistroBazzite:
		return NewBazziteImage(p, BazziteVersion(version)), nil
	default:
		return nil, fmt.Errorf("unknown distro: %s", distro)
	}
}
