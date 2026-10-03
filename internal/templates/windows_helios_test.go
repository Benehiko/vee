package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Benehiko/vee/internal/vm"
	"github.com/Benehiko/vee/provider"
)

func TestGuestSetupPS1Helios(t *testing.T) {
	plain := guestSetupPS1("share", nil, false)
	if strings.Contains(plain, "Helios") || strings.Contains(plain, "{{HELIOS}}") {
		t.Fatalf("non-helios script mentions Helios or leaks the placeholder:\n%s", plain)
	}
	s := guestSetupPS1("share", nil, true)
	for _, want := range []string{
		`HeliosSetup.exe`,
		`'--silent', '--automatic'`,
		`VeeHeliosWatch`,
		heliosWatchName,
		`AutoAdminLogon -Value '1'`,
		`DefaultUserName -Value '` + winAdminUser + `'`,
		`Remove-ItemProperty -Path $wl -Name AutoLogonCount`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("helios setup script missing %q", want)
		}
	}
	// Persistent auto-logon must be set before the installer's reboots.
	if strings.Index(s, "AutoAdminLogon") > strings.Index(s, "installing Helios") {
		t.Error("auto-logon is configured after the Helios install starts")
	}
	if strings.Contains(plain, "AutoAdminLogon") {
		t.Error("plain windows VMs must keep the answer file's one-shot auto-logon")
	}
	// Helios must run before the closing reboot, or it never runs at all.
	if strings.Index(s, "installing Helios") > strings.Index(s, "Restart-Computer -Force") {
		t.Error("Helios install runs after the closing Restart-Computer")
	}
	if strings.Contains(s, "{{") {
		t.Errorf("unrendered placeholder in script:\n%s", s)
	}
}

func TestHeliosWatchRebootsOncePerStatus(t *testing.T) {
	// Guard the loop-prevention logic: a status already handled must be
	// skipped, and reboots must be capped.
	for _, want := range []string{"if ($s -eq $last) { continue }", "$count -ge 4", "Unregister-ScheduledTask"} {
		if !strings.Contains(heliosWatchPS1, want) {
			t.Errorf("watch script missing %q", want)
		}
	}
}

func TestApplyHelios(t *testing.T) {
	cfg := &vm.VMConfig{
		Name:    "wintest",
		Globals: []string{"driver=cfi.pflash01,property=secure,value=on"},
		SPICE:   &vm.SPICEConfig{Port: 5930},
		UEFI:    vm.UEFIConfig{Enabled: true, CodePath: "/ovmf/OVMF_CODE.secboot.fd"},
	}
	conf := &provider.Config{OVMFCodePath: "/ovmf/OVMF_CODE.fd"}
	applyHelios(cfg, conf, &HeliosOptions{QemuBinary: "/opt/qemu-helios/bin/qemu-system-x86_64", HostMem: "12G"})

	if cfg.UEFI.CodePath != "/ovmf/OVMF_CODE.fd" {
		t.Errorf("UEFI code = %q, want the non-secboot build", cfg.UEFI.CodePath)
	}
	if len(cfg.Globals) != 0 {
		t.Errorf("secure pflash global kept: %v", cfg.Globals)
	}
	if cfg.SPICE != nil {
		t.Error("SPICE kept on a helios VM")
	}
	if cfg.GPU.Mode != vm.GPUHelios || cfg.GPU.HostMem != "12G" {
		t.Errorf("GPU = %+v", cfg.GPU)
	}
	if cfg.GPU.VNC != heliosVNCAddr("wintest") {
		t.Errorf("VNC = %q, want %q", cfg.GPU.VNC, heliosVNCAddr("wintest"))
	}
	if cfg.QemuBinary == "" {
		t.Error("qemu binary not recorded")
	}
}

func TestHeliosOptionsValidate(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "HeliosSetup.exe")
	qemuBin := filepath.Join(dir, "qemu-system-x86_64")
	for _, f := range []string{exe, qemuBin} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ok := HeliosOptions{SetupExe: exe, QemuBinary: qemuBin}
	if err := ok.validate(); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	for name, h := range map[string]HeliosOptions{
		"no setup":     {QemuBinary: qemuBin},
		"zip not exe":  {SetupExe: filepath.Join(dir, "helios.zip"), QemuBinary: qemuBin},
		"missing exe":  {SetupExe: filepath.Join(dir, "nope.exe"), QemuBinary: qemuBin},
		"no qemu":      {SetupExe: exe},
		"missing qemu": {SetupExe: exe, QemuBinary: filepath.Join(dir, "nope")},
	} {
		if err := h.validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestApplyHeliosBootstrapVGA(t *testing.T) {
	cfg := &vm.VMConfig{Name: "w"}
	applyHelios(cfg, &provider.Config{}, &HeliosOptions{QemuBinary: "/q"})
	if len(cfg.InstallDevices) != 0 {
		t.Errorf("bootstrap VGA attached without being asked: %v", cfg.InstallDevices)
	}
	cfg = &vm.VMConfig{Name: "w"}
	applyHelios(cfg, &provider.Config{}, &HeliosOptions{QemuBinary: "/q", BootstrapVGA: true})
	if len(cfg.InstallDevices) != 1 || cfg.InstallDevices[0] != heliosBootstrapVGADevice {
		t.Errorf("InstallDevices = %v", cfg.InstallDevices)
	}
	if len(cfg.ExtraDevices) != 0 {
		t.Error("bootstrap VGA must be install-only, not an extra device")
	}
}
