package vm

import (
	"runtime"
	"testing"
	"time"

	"github.com/Benehiko/vee/internal/platform"
)

func TestParseCPUPinning(t *testing.T) {
	cases := []struct {
		in      string
		want    []int
		wantErr bool
	}{
		{in: "", want: nil},
		{in: "  ", want: nil},
		{in: "4,5,6,7", want: []int{4, 5, 6, 7}},
		{in: " 2 , 3 ,4 ", want: []int{2, 3, 4}},
		{in: "2,3,2", want: []int{2, 3}},
		{in: "4,x,6", wantErr: true},
		{in: "4,-1,6", wantErr: true},
	}
	for _, tc := range cases {
		got, err := ParseCPUPinning(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseCPUPinning(%q): want error, got %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseCPUPinning(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("ParseCPUPinning(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("ParseCPUPinning(%q) = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}

func TestSetCPUPinningPersistsWhenStopped(t *testing.T) {
	if !platform.SupportsCPUPinning() {
		t.Skip("CPU pinning not supported on this platform")
	}
	if runtime.NumCPU() < 2 {
		t.Skip("test requires at least 2 host CPUs")
	}
	m := newTestManager(t)
	cfg := &VMConfig{Name: "gaming", Template: "gaming-arch", CreatedAt: time.Now()}
	if err := m.saveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	want := []int{0, 1}
	if err := m.SetCPUPinning("gaming", want); err != nil {
		t.Fatalf("SetCPUPinning: %v", err)
	}
	got, err := m.loadConfig("gaming")
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(got.CPUPinning) != len(want) {
		t.Errorf("persisted CPUPinning = %v, want %v", got.CPUPinning, want)
	}
}

func TestSetCPUPinningRejectsOutOfRange(t *testing.T) {
	if !platform.SupportsCPUPinning() {
		t.Skip("CPU pinning not supported on this platform")
	}
	m := newTestManager(t)
	cfg := &VMConfig{Name: "gaming", Template: "gaming-arch", CreatedAt: time.Now()}
	if err := m.saveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := m.SetCPUPinning("gaming", []int{runtime.NumCPU() + 10}); err == nil {
		t.Error("out-of-range CPU index: want validation error")
	}
}

func TestSetCPUPinningUnknownVM(t *testing.T) {
	if !platform.SupportsCPUPinning() {
		t.Skip("CPU pinning not supported on this platform")
	}
	m := newTestManager(t)
	if err := m.SetCPUPinning("ghost", []int{0, 1}); err == nil {
		t.Error("unknown VM: want a not-found error")
	}
}

func TestSetCPUPinningUnsupportedPlatform(t *testing.T) {
	if platform.SupportsCPUPinning() {
		t.Skip("this test targets platforms without CPU pinning support")
	}
	m := newTestManager(t)
	cfg := &VMConfig{Name: "gaming", Template: "gaming-arch", CreatedAt: time.Now()}
	if err := m.saveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := m.SetCPUPinning("gaming", []int{0, 1}); err == nil {
		t.Error("unsupported platform: want error")
	}
}
