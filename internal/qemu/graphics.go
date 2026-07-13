package qemu

import (
	"fmt"
	"strings"
)

// GLBackend selects the host OpenGL translation backend used by the -display
// gl= suboption.
//
// On macOS there is no native EGL/GBM stack and OpenGL is deprecated (frozen at
// 4.1), so virglrenderer needs a translation layer: "es" routes GLES through
// ANGLE onto Metal (the stable, recommended path), while "core" uses the native
// OpenGL core profile (unstable). On Linux "on" uses the host EGL stack.
type GLBackend string

const (
	// GLBackendOff disables GL on the display backend.
	GLBackendOff GLBackend = "off"
	// GLBackendOn enables host GL (Linux EGL).
	GLBackendOn GLBackend = "on"
	// GLBackendES routes GLES via ANGLE onto Metal (macOS, stable/fast).
	GLBackendES GLBackend = "es"
	// GLBackendCore uses the native OpenGL core profile (macOS, unstable).
	GLBackendCore GLBackend = "core"
)

// VirtioGPUDevice returns the -device value for a virtio-gpu adapter suitable
// for the given guest architecture.
//
//   - aarch64 (the "virt" board has no VGA) uses virtio-gpu-gl-pci for GL and
//     virtio-gpu-pci otherwise.
//   - x86_64 ("q35") uses virtio-vga-gl (a VGA-compatible variant) for GL and
//     virtio-gpu-pci otherwise.
//
// When gl is true the GL-capable variant is selected. When venus is also true
// the Vulkan-over-virtio (Venus) path is enabled, which requires blob resources
// and a host memory window sized by hostMem (e.g. "8G"); an empty hostMem omits
// the suboption and lets QEMU pick its default.
func VirtioGPUDevice(arch string, gl, venus bool, hostMem string) string {
	var dev string
	switch {
	case !gl:
		dev = "virtio-gpu-pci"
	case arch == "aarch64" || arch == "arm64":
		dev = "virtio-gpu-gl-pci"
	default:
		dev = "virtio-vga-gl"
	}
	if gl && venus {
		opts := []string{"blob=true", "venus=true"}
		if hostMem != "" {
			opts = append(opts, "hostmem="+hostMem)
		}
		dev += "," + strings.Join(opts, ",")
	}
	return dev
}

// DisplayArg returns the -display value for the given host OS. macOS only has
// the cocoa windowed backend; Linux uses gtk. When gl is true the gl= suboption
// is appended using backend (empty picks the host default: es on macOS, on
// Linux). When gl is false a plain windowed display is returned.
func DisplayArg(hostOS string, gl bool, backend GLBackend) string {
	base := "gtk"
	if hostOS == "darwin" {
		base = "cocoa"
	}
	if !gl {
		return base
	}
	if backend == "" {
		backend = DefaultGLBackend(hostOS)
	}
	return fmt.Sprintf("%s,gl=%s", base, backend)
}

// DefaultGLBackend returns the recommended GL backend for a host OS: the stable
// ANGLE/Metal "es" path on macOS, host EGL "on" elsewhere.
func DefaultGLBackend(hostOS string) GLBackend {
	if hostOS == "darwin" {
		return GLBackendES
	}
	return GLBackendOn
}
