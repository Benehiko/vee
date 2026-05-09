package qemu_test

import (
	"strings"
	"testing"

	"go.uber.org/goleak"
	"go.uber.org/zap"

	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/qemu"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// testProvider is a minimal provider.Provider for unit tests.
type testProvider struct {
	cfg *provider.Config
	log *zap.Logger
}

func newTestProvider(t *testing.T) provider.Provider {
	t.Helper()
	return &testProvider{
		cfg: &provider.Config{
			QemuBinaryPath:  "qemu-system-x86_64",
			VirtiofsdPath:   "/usr/bin/virtiofsd",
			DefaultMemory:   "2G",
			DefaultCPUs:     2,
			DefaultDiskSize: "20G",
			DefaultCPUModel: "host",
			DefaultMachineType: "q35",
			StoragePath:     t.TempDir(),
		},
		log: zap.NewNop(),
	}
}

func (p *testProvider) Config() *provider.Config { return p.cfg }
func (p *testProvider) Logger() *zap.Logger      { return p.log }

func TestDeterministicMAC(t *testing.T) {
	mac1 := qemu.DeterministicMAC("my-vm")
	mac2 := qemu.DeterministicMAC("my-vm")
	if mac1 != mac2 {
		t.Errorf("MAC not deterministic: %q != %q", mac1, mac2)
	}

	mac3 := qemu.DeterministicMAC("other-vm")
	if mac1 == mac3 {
		t.Error("different names produced the same MAC")
	}

	parts := strings.Split(mac1, ":")
	if len(parts) != 6 {
		t.Errorf("expected 6 octets, got %d in %q", len(parts), mac1)
	}
	if !strings.HasPrefix(mac1, "52:54:") {
		t.Errorf("MAC missing QEMU prefix 52:54: got %q", mac1)
	}
}

func TestNICArgsUser(t *testing.T) {
	nic := qemu.NewNIC(qemu.NICUser, "", "52:54:ab:cd:ef:01")
	args := nic.Args()
	if len(args) != 2 || args[0] != "-nic" {
		t.Fatalf("unexpected args: %v", args)
	}
	val := args[1]
	if !strings.HasPrefix(val, "user,") {
		t.Errorf("user NIC should start with 'user,': %q", val)
	}
	if !strings.Contains(val, "mac=52:54:ab:cd:ef:01") {
		t.Errorf("MAC missing from NIC arg: %q", val)
	}
}

func TestNICArgsUserHostfwd(t *testing.T) {
	nic := qemu.NewNIC(qemu.NICUser, "", "52:54:00:00:00:01", "tcp:127.0.0.1:2222-:22")
	args := nic.Args()
	val := args[1]
	if !strings.Contains(val, "hostfwd=tcp:127.0.0.1:2222-:22") {
		t.Errorf("hostfwd missing from NIC arg: %q", val)
	}
}

func TestNICArgsBridge(t *testing.T) {
	nic := qemu.NewNIC(qemu.NICBridge, "br0", "52:54:00:00:00:02")
	args := nic.Args()
	val := args[1]
	if !strings.HasPrefix(val, "bridge,") {
		t.Errorf("bridge NIC should start with 'bridge,': %q", val)
	}
	if !strings.Contains(val, "br=br0") {
		t.Errorf("bridge name missing: %q", val)
	}
}

func TestMemfdBackendArgs(t *testing.T) {
	m := qemu.NewMemfdBackend("4G")
	args := m.Args()
	if len(args) != 4 {
		t.Fatalf("expected 4 args, got %d: %v", len(args), args)
	}
	if args[0] != "-object" {
		t.Errorf("expected -object, got %q", args[0])
	}
	if !strings.Contains(args[1], "size=4G") {
		t.Errorf("size missing from memfd arg: %q", args[1])
	}
	if args[2] != "-numa" {
		t.Errorf("expected -numa, got %q", args[2])
	}
}
