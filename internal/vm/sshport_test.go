package vm

import (
	"strings"
	"testing"
	"time"
)

// TestSetSSHPortPersistsWhenStopped covers the create-time-only hole: changing
// the port on a stopped VM must persist to the config so the next start picks
// it up, and must report applied=false (nothing was running to apply to).
func TestSetSSHPortPersistsWhenStopped(t *testing.T) {
	m := newTestManager(t)
	cfg := &VMConfig{
		Name:      "win",
		Template:  "windows",
		SSHPort:   2244,
		NIC:       NICConfig{Mode: "user"},
		CreatedAt: time.Now(),
	}
	if err := m.saveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	applied, err := m.SetSSHPort(t.Context(), "win", 2299)
	if err != nil {
		t.Fatalf("SetSSHPort: %v", err)
	}
	if applied {
		t.Error("applied=true for a stopped VM; nothing was running to apply to")
	}
	got, err := m.loadConfig("win")
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got.SSHPort != 2299 {
		t.Errorf("persisted SSHPort = %d, want 2299", got.SSHPort)
	}
}

func TestSetSSHPortRejectsVZ(t *testing.T) {
	m := newTestManager(t)
	cfg := &VMConfig{Name: "linux-vz", Backend: "vz", CreatedAt: time.Now()}
	if err := m.saveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if _, err := m.SetSSHPort(t.Context(), "linux-vz", 2222); err == nil || !strings.Contains(err.Error(), "vz backend") {
		t.Errorf("vz backend: got err %v, want vz refusal", err)
	}
}

func TestSetSSHPortValidatesPort(t *testing.T) {
	m := newTestManager(t)
	for _, port := range []int{0, -1, 70000} {
		if _, err := m.SetSSHPort(t.Context(), "any", port); err == nil {
			t.Errorf("port %d: want validation error", port)
		}
	}
}

func TestSetSSHPortUnknownVM(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.SetSSHPort(t.Context(), "ghost", 2222); err == nil {
		t.Error("unknown VM: want a not-found error")
	}
}
