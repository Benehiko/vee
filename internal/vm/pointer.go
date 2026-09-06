package vm

import (
	"fmt"

	"github.com/Benehiko/vee/internal/qemu"
)

// ValidatePointer accepts the empty string (keep the default) and the two
// pointer devices the virtio-GPU path knows how to attach.
func ValidatePointer(p string) error {
	switch qemu.PointerDevice(p) {
	case "", qemu.PointerTablet, qemu.PointerMouse:
		return nil
	}
	return fmt.Errorf("unknown pointer %q: use %s (absolute, desktop default) or %s (relative, for pointer-locked games)", p, qemu.PointerTablet, qemu.PointerMouse)
}

// SetPointer records the pointing device for a virtio-GPU VM. QEMU cannot swap
// an input device on a running machine, so the change applies on the next
// start.
func (m *Manager) SetPointer(name, pointer string) error {
	if err := ValidatePointer(pointer); err != nil {
		return err
	}
	cfg, err := m.loadConfig(name)
	if err != nil {
		return err
	}
	if cfg.GPU.Mode != GPUVirtio {
		return fmt.Errorf("pointer applies to GPU mode %q only (this VM is %q)", GPUVirtio, cfg.GPU.Mode)
	}
	cfg.GPU.Pointer = pointer
	return m.saveConfig(cfg)
}
