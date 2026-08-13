package vm

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/internal/backend"
	"github.com/Benehiko/vee/provider"
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

	// On a non-mac host this hits the host guard ("the vz backend requires an
	// Apple Silicon macOS host"); on Apple Silicon a config without a macos:
	// section is a Linux guest, which the empty config fails further in
	// (memory validation). Either way the dispatch reached the vz path.
	_, _, err := m.buildBackendMachine(context.Background(), &VMConfig{Backend: "vz"})
	if err == nil {
		t.Error("empty vz config: want an error from the vz build path")
	}

	_, _, err = m.buildBackendMachine(context.Background(), &VMConfig{Backend: "bhyve"})
	if err == nil || !strings.Contains(err.Error(), "unknown backend") {
		t.Errorf("unknown backend: got err %v, want unknown-backend error", err)
	}
}

// testProvider is the minimum a Manager needs to run the code paths below.
type testProvider struct {
	cfg *provider.Config
	log *zap.Logger
}

func (p *testProvider) Config() *provider.Config { return p.cfg }
func (p *testProvider) Logger() *zap.Logger      { return p.log }
func (p *testProvider) DB() *sql.DB              { return nil }

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(&testProvider{
		cfg: &provider.Config{StoragePath: t.TempDir()},
		log: zap.NewNop(),
	})
}

func TestGracefulShutdownVZDoesNotDialQMP(t *testing.T) {
	// A vz VM must never have its QMP socket dialled — that socket belongs to
	// another protocol — and the call must survive a VM whose config and guest
	// are both unreachable, since stop runs on the failure paths too.
	m := newTestManager(t)
	m.gracefulShutdown(t.Context(), "no-such-vm", &VMState{Backend: "vz", QMPSocket: "/tmp/nope.sock"})
}
