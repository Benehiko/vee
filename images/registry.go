package images

import (
	"fmt"

	"github.com/Benehiko/vee/provider"
)

const (
	DistroUbuntu = "ubuntu"
	DistroArch   = "arch"
	DistroFedora = "fedora"
)

// SupportedDistros returns all known distro slugs.
func SupportedDistros() []string {
	return []string{DistroUbuntu, DistroArch, DistroFedora}
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
	default:
		return nil
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
		return NewUbuntuImage(p, UbuntuServer, UbuntuVersion(version), "amd64"), nil
	case DistroArch:
		return NewArchImage(p, ArchVersion(version)), nil
	case DistroFedora:
		return NewFedoraImage(p, FedoraVersion(version)), nil
	default:
		return nil, fmt.Errorf("unknown distro: %s", distro)
	}
}
