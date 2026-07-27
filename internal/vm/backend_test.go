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
	// The vz and unknown-backend branches must fail before touching any
	// manager internals, so a zero Manager is enough here.
	m := &Manager{}

	_, _, err := m.buildBackendMachine(context.Background(), &VMConfig{Backend: "vz"})
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Errorf("vz backend: got err %v, want not-implemented error", err)
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
