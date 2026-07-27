package vm

import (
	"context"
	"strings"
	"testing"

	"github.com/Benehiko/vee/internal/backend"
)

func TestVMConfigBackendName(t *testing.T) {
	tests := []struct {
		field string
		want  backend.Name
	}{
		{"", backend.QEMU}, // legacy configs default to QEMU
		{"qemu", backend.QEMU},
		{"vz", backend.VZ},
	}
	for _, tt := range tests {
		cfg := &VMConfig{Backend: tt.field}
		if got := cfg.BackendName(); got != tt.want {
			t.Errorf("VMConfig{Backend: %q}.BackendName() = %q, want %q", tt.field, got, tt.want)
		}
	}
}

func TestVMStateBackendName(t *testing.T) {
	var nilState *VMState
	if got := nilState.BackendName(); got != backend.QEMU {
		t.Errorf("nil state BackendName() = %q, want %q", got, backend.QEMU)
	}
	if got := (&VMState{}).BackendName(); got != backend.QEMU {
		t.Errorf("legacy state BackendName() = %q, want %q", got, backend.QEMU)
	}
	if got := (&VMState{Backend: "vz"}).BackendName(); got != backend.VZ {
		t.Errorf("vz state BackendName() = %q, want %q", got, backend.VZ)
	}
}

func TestBuildBackendMachineDispatch(t *testing.T) {
	// The early vz guards (host check, missing macos: section) and the
	// unknown-backend branch all fail before touching manager internals, so
	// a zero Manager is enough here.
	m := &Manager{}

	// On a non-mac host this hits the host guard; on Apple Silicon it hits
	// the missing macos: section guard. Both are "the vz backend requires"
	// errors.
	_, _, err := m.buildBackendMachine(context.Background(), &VMConfig{Backend: "vz"})
	if err == nil || !strings.Contains(err.Error(), "vz backend requires") {
		t.Errorf("vz backend without prerequisites: got err %v, want vz-requirements error", err)
	}

	_, _, err = m.buildBackendMachine(context.Background(), &VMConfig{Backend: "bhyve"})
	if err == nil || !strings.Contains(err.Error(), "unknown backend") {
		t.Errorf("unknown backend: got err %v, want unknown-backend error", err)
	}
}

func TestGracefulShutdownNonQEMUIsNoop(t *testing.T) {
	// A vz-backend state must not dial QMP (there is nothing to dial yet);
	// the call must be a safe no-op even on a zero Manager.
	m := &Manager{}
	m.gracefulShutdown(context.Background(), "some-vm", &VMState{Backend: "vz", QMPSocket: "/tmp/nope.sock"})
}
