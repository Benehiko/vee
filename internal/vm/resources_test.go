package vm

import (
	"testing"
	"time"
)

func TestSetMemoryPersists(t *testing.T) {
	m := newTestManager(t)
	cfg := &VMConfig{Name: "gaming", Template: "gaming-arch", Memory: "16G", CreatedAt: time.Now()}
	if err := m.saveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := m.SetMemory("gaming", "10G"); err != nil {
		t.Fatalf("SetMemory: %v", err)
	}
	got, err := m.loadConfig("gaming")
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got.Memory != "10G" {
		t.Errorf("persisted Memory = %q, want %q", got.Memory, "10G")
	}
}

func TestSetMemoryRejectsEmpty(t *testing.T) {
	m := newTestManager(t)
	cfg := &VMConfig{Name: "gaming", Template: "gaming-arch", CreatedAt: time.Now()}
	if err := m.saveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := m.SetMemory("gaming", ""); err == nil {
		t.Error("empty memory: want validation error")
	}
}

func TestSetMemoryUnknownVM(t *testing.T) {
	m := newTestManager(t)
	if err := m.SetMemory("ghost", "10G"); err == nil {
		t.Error("unknown VM: want a not-found error")
	}
}

func TestSetCPUsPersists(t *testing.T) {
	m := newTestManager(t)
	cfg := &VMConfig{Name: "gaming", Template: "gaming-arch", CPUs: 8, CreatedAt: time.Now()}
	if err := m.saveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := m.SetCPUs("gaming", 4); err != nil {
		t.Fatalf("SetCPUs: %v", err)
	}
	got, err := m.loadConfig("gaming")
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got.CPUs != 4 {
		t.Errorf("persisted CPUs = %d, want 4", got.CPUs)
	}
}

func TestSetCPUsRejectsNonPositive(t *testing.T) {
	m := newTestManager(t)
	cfg := &VMConfig{Name: "gaming", Template: "gaming-arch", CreatedAt: time.Now()}
	if err := m.saveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	for _, n := range []int{0, -1} {
		if err := m.SetCPUs("gaming", n); err == nil {
			t.Errorf("SetCPUs(%d): want validation error", n)
		}
	}
}

func TestSetCPUsUnknownVM(t *testing.T) {
	m := newTestManager(t)
	if err := m.SetCPUs("ghost", 4); err == nil {
		t.Error("unknown VM: want a not-found error")
	}
}

func TestSetNICModePersists(t *testing.T) {
	m := newTestManager(t)
	cfg := &VMConfig{Name: "gaming", Template: "gaming-arch", NIC: NICConfig{Mode: "user"}, CreatedAt: time.Now()}
	if err := m.saveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := m.SetNICMode("gaming", "bridge", ""); err != nil {
		t.Fatalf("SetNICMode: %v", err)
	}
	got, err := m.loadConfig("gaming")
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got.NIC.Mode != "bridge" {
		t.Errorf("persisted NIC.Mode = %q, want %q", got.NIC.Mode, "bridge")
	}
	if got.NIC.Bridge != "br0" {
		t.Errorf("persisted NIC.Bridge = %q, want default %q", got.NIC.Bridge, "br0")
	}
}

func TestSetNICModeCustomBridge(t *testing.T) {
	m := newTestManager(t)
	cfg := &VMConfig{Name: "gaming", Template: "gaming-arch", NIC: NICConfig{Mode: "user"}, CreatedAt: time.Now()}
	if err := m.saveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := m.SetNICMode("gaming", "bridge", "br1"); err != nil {
		t.Fatalf("SetNICMode: %v", err)
	}
	got, err := m.loadConfig("gaming")
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got.NIC.Bridge != "br1" {
		t.Errorf("persisted NIC.Bridge = %q, want %q", got.NIC.Bridge, "br1")
	}
}

func TestSetNICModeRejectsInvalid(t *testing.T) {
	m := newTestManager(t)
	cfg := &VMConfig{Name: "gaming", Template: "gaming-arch", CreatedAt: time.Now()}
	if err := m.saveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := m.SetNICMode("gaming", "carrier-pigeon", ""); err == nil {
		t.Error("invalid nic mode: want validation error")
	}
}

func TestSetNICModeUnknownVM(t *testing.T) {
	m := newTestManager(t)
	if err := m.SetNICMode("ghost", "bridge", ""); err == nil {
		t.Error("unknown VM: want a not-found error")
	}
}
