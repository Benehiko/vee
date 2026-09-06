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

// DefaultVenusHostMem is the host memory window applied to the virtio-gpu-gl
// device when Venus is enabled without an explicit size. It sizes the window
// for blob resources (the shared buffers virglrenderer uses to hand Vulkan
// allocations between host and guest), so it scales with the GPU working set —
// render resolution, texture sizes, buffers in flight — and deliberately not
// with guest RAM, which is unrelated.
const DefaultVenusHostMem = "8G"

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
// and a host memory window sized by hostMem (e.g. "8G"); an empty hostMem falls
// back to DefaultVenusHostMem, since QEMU's own default window is often too
// small and Venus then fails in ways that are hard to trace back to sizing.
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
		if hostMem == "" {
			hostMem = DefaultVenusHostMem
		}
		opts := []string{"blob=true", "venus=true", "hostmem=" + hostMem}
		dev += "," + strings.Join(opts, ",")
	}
	return dev
}

// PointerDevice selects the virtio pointing device attached next to the
// virtio keyboard.
type PointerDevice string

const (
	// PointerTablet is an absolute pointer (virtio-tablet-pci): the host cursor
	// maps 1:1 onto the guest screen and the host window never grabs it, which
	// is right for a desktop. Wayland compositors deliver absolute motion with
	// a zero relative delta, so a pointer-locked game (mouse-look) sees the
	// cursor but no movement.
	PointerTablet PointerDevice = "tablet"
	// PointerMouse is a relative pointer (virtio-mouse-pci): the host window
	// grabs the cursor on click and forwards motion deltas, which is what
	// pointer-locked games read. Ctrl+Alt+G releases the grab in the QEMU
	// window.
	PointerMouse PointerDevice = "mouse"
)

// VirtioInputDevices returns the -device values for a virtio keyboard and
// tablet (absolute pointer), the desktop default; see VirtioInputDevicesFor.
func VirtioInputDevices() []string {
	return VirtioInputDevicesFor(PointerTablet)
}

// VirtioInputDevicesFor returns the -device values for a virtio keyboard and
// the given pointer. Boards without built-in input — the aarch64 "virt" board
// has no PS/2 controller, unlike x86 "q35" — drop every host window click and
// keystroke unless explicit input devices are attached. The virtio-input
// drivers ship in the Linux kernel; Windows guests need USB HID devices
// instead. An empty or unknown pointer selects the tablet.
func VirtioInputDevicesFor(pointer PointerDevice) []string {
	dev := "virtio-tablet-pci"
	if pointer == PointerMouse {
		dev = "virtio-mouse-pci"
	}
	return []string{"virtio-keyboard-pci", dev}
}

// DisplayArg returns the -display value for the given host OS with the
// default (tablet) pointer; see DisplayArgFor.
func DisplayArg(hostOS string, gl bool, backend GLBackend) string {
	return DisplayArgFor(hostOS, gl, backend, PointerTablet)
}

// DisplayArgFor returns the -display value for the given host OS and pointer
// device. macOS only has the cocoa windowed backend. Linux uses gtk, except
// with a relative pointer: GTK3 cannot lock or warp the cursor on a Wayland
// host, so its mouse grab never captures motion and a virtio-mouse gets no
// deltas — the SDL2 display locks the pointer properly (relative mouse mode
// on both Wayland and X11), so PointerMouse selects sdl. When gl is true the
// gl= suboption is appended using backend (empty picks the host default: es
// on macOS, on elsewhere). When gl is false a plain windowed display is
// returned.
func DisplayArgFor(hostOS string, gl bool, backend GLBackend, pointer PointerDevice) string {
	base := "gtk"
	if hostOS == "darwin" {
		base = "cocoa"
	} else if pointer == PointerMouse {
		base = "sdl"
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
