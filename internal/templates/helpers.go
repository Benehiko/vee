package templates

import (
	"fmt"
	"hash/fnv"

	"github.com/Benehiko/vee/internal/images"
	"github.com/Benehiko/vee/internal/platform"
)

// deterministicSSHPort maps a VM name to a stable host port in [2200, 2299].
func deterministicSSHPort(name string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return 2200 + int(h.Sum32()%100)
}

// guestArchFor gates a distro's image against the host architecture: a
// native image needs nothing, while a cross-arch image (an x86_64-only ISO on
// an Apple Silicon host) needs the explicit --emulate opt-in so nobody lands
// in a TCG-emulated guest by accident. It returns the value for
// vm.VMConfig.Arch — empty for native images, the image's arch when
// emulating — so callers can assign it directly. Call it before downloading
// the image, so a refusal costs nothing.
func guestArchFor(distro string, emulate bool) (string, error) {
	return guestArchOn(platform.DefaultGuestArch(), images.ImageArch(distro), distro, emulate)
}

// guestArchOn is guestArchFor with the host and image arches injected, so the
// decision is testable off the host it happens to run on.
func guestArchOn(hostArch, imageArch, distro string, emulate bool) (string, error) {
	if imageArch == hostArch {
		return "", nil
	}
	if !emulate {
		return "", fmt.Errorf("the %s image is %s-only and this host's native guest arch is %s — "+
			"pass --emulate to run it under TCG emulation (functional, but slower than a native guest)",
			distro, imageArch, hostArch)
	}
	return imageArch, nil
}
