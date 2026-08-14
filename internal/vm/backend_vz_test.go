package vm

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.uber.org/zap/zapcore"

	"github.com/Benehiko/vee/internal/platform"
	"github.com/Benehiko/vee/internal/vzhelper"
	"github.com/Benehiko/vee/provider"
)

func TestVZShutdownArgs(t *testing.T) {
	args := vzShutdownArgs("vee", "192.168.64.9", "/Users/someone")

	// The command must be able to run unattended: a prompt would hang `vee
	// stop` until its grace period expired, which is the bug this fixes.
	for _, required := range []string{"BatchMode=yes", "ConnectTimeout=5"} {
		if !slices.Contains(args, required) {
			t.Errorf("missing %q in %v", required, args)
		}
	}
	if !slices.Contains(args, "vee@192.168.64.9") {
		t.Errorf("destination missing from %v", args)
	}

	// sudo -n so a guest without the sudoers rule fails fast instead of
	// waiting for a password nobody can type.
	last := args[len(args)-1]
	if !strings.Contains(last, "sudo -n /sbin/shutdown -h now") {
		t.Errorf("remote command = %q, want a non-interactive shutdown", last)
	}

	// The vee-managed key and known_hosts, not the user's personal ones.
	joined := strings.Join(args, " ")
	for _, want := range []string{"/Users/someone/.vee/ssh/id_ed25519", "/Users/someone/.vee/ssh/known_hosts"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
}

func TestVZRawDiskPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/vms/a/storage/disk-os.qcow2", "/vms/a/storage/disk-os.raw"},
		{"/vms/a/storage/disk-os.img", "/vms/a/storage/disk-os.raw"},
		{"/vms/a/storage/disk-os.raw", "/vms/a/storage/disk-os.raw"},
		// A bare directory is qemu.Disk's "generate the name" form.
		{"/mnt/nvme", "/mnt/nvme/disk-os.raw"},
	}
	for _, tt := range tests {
		if got := vzRawDiskPath(tt.in); got != tt.want {
			t.Errorf("vzRawDiskPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// newVZTestManager builds a Manager whose provider only carries a logger and
// a temp storage path — all the vz create/spec helpers need.
func newVZTestManager(t *testing.T) *Manager {
	t.Helper()
	entries := &[]zapcore.Entry{}
	return &Manager{provider: grantProvider{
		cfg:     &provider.Config{StoragePath: t.TempDir()},
		entries: entries,
	}}
}

func TestVZDiskSpecs(t *testing.T) {
	m := newVZTestManager(t)
	dir := t.TempDir()
	raw := filepath.Join(dir, "disk-os.raw")
	cidata := filepath.Join(dir, "cidata.iso")
	for _, p := range []string{raw, cidata} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("cdrom attaches read-only, missing cdrom is skipped", func(t *testing.T) {
		cfg := &VMConfig{Name: "lin", Disks: []DiskConfig{
			{Path: raw, Format: "raw", Media: "disk"},
			{Path: cidata, Media: "cdrom"},
			// The install-state machine strips a consumed seed from the
			// config, but a stale config can still name a deleted one.
			{Path: filepath.Join(dir, "gone.iso"), Media: "cdrom"},
		}}
		specs, err := m.vzDiskSpecs(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if len(specs) != 2 {
			t.Fatalf("got %d disk specs, want 2 (missing cdrom skipped): %+v", len(specs), specs)
		}
		if specs[0].ReadOnly {
			t.Error("boot disk must not be read-only")
		}
		if !specs[1].ReadOnly {
			t.Error("cdrom seed must attach read-only")
		}
	})

	rejected := []struct {
		name string
		disk DiskConfig
	}{
		{"qcow2 format", DiskConfig{Path: raw, Format: "qcow2", Media: "disk"}},
		{"relative path", DiskConfig{Path: "storage/disk-os.raw", Format: "raw", Media: "disk"}},
		{"passthrough device", DiskConfig{Path: "/dev/disk4", Format: "raw", Media: "disk", Passthrough: true}},
	}
	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &VMConfig{Name: "lin", Disks: []DiskConfig{tt.disk}}
			if _, err := m.vzDiskSpecs(cfg); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

// The create-time guards refuse QEMU-only devices with actionable errors and
// drop the harmless template defaults (SPICE, ssh_port, OVMF UEFI) that have
// vz-native replacements.
func TestPrepareVZLinuxCreate(t *testing.T) {
	if !platform.IsMacOS() || platform.HostArch() != "arm64" {
		t.Skip("prepareVZLinuxCreate requires an Apple Silicon macOS host")
	}
	m := newVZTestManager(t)
	ctx := context.Background()

	rejected := []struct {
		name   string
		mutate func(*VMConfig)
	}{
		{"virtio gpu", func(c *VMConfig) { c.GPU.Mode = GPUVirtio }},
		{"tpm", func(c *VMConfig) { c.TPM = &TPMConfig{Enabled: true} }},
		{"virtiofs share", func(c *VMConfig) {
			c.VirtiofsMounts = []VirtiofsMount{{SharedDir: "/tmp", Tag: "share"}}
		}},
		{"bridge nic", func(c *VMConfig) { c.NIC.Mode = "bridge" }},
		{"passthrough disk", func(c *VMConfig) {
			c.Disks = []DiskConfig{{Path: "/dev/disk4", Media: "disk", Passthrough: true}}
		}},
	}
	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &VMConfig{Name: "lin", Backend: "vz", NIC: NICConfig{Mode: "user"}}
			tt.mutate(cfg)
			if err := m.prepareVZLinuxCreate(ctx, cfg); err == nil {
				t.Error("expected an error")
			}
		})
	}

	t.Run("drops SPICE, spice services and ssh_port; clears UEFI", func(t *testing.T) {
		cfg := &VMConfig{
			Name: "lin", Backend: "vz",
			NIC:     NICConfig{Mode: "user"},
			SPICE:   &SPICEConfig{Port: 5900},
			SSHPort: 2201,
			UEFI:    UEFIConfig{Enabled: true},
			Services: []ServiceEntry{
				{Name: "display", Port: 5900, Protocol: ServiceSPICE},
				{Name: "web", Port: 8080, Protocol: ServiceHTTP},
			},
		}
		if err := m.prepareVZLinuxCreate(ctx, cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.SPICE != nil {
			t.Error("SPICE was not dropped")
		}
		if cfg.SSHPort != 0 {
			t.Error("ssh_port was not dropped")
		}
		if cfg.UEFI.Enabled {
			t.Error("OVMF UEFI was not cleared (vz carries its own EFI variable store)")
		}
		if len(cfg.Services) != 1 || cfg.Services[0].Protocol != ServiceHTTP {
			t.Errorf("services = %+v, want only the non-SPICE entry", cfg.Services)
		}
	})
}

// The Linux branch of buildVZMachine writes a spec the helper can consume:
// linux platform, EFI variable store and serial log inside the VM directory,
// no macOS restore artifacts.
func TestBuildVZMachineLinuxSpec(t *testing.T) {
	if !platform.IsMacOS() || platform.HostArch() != "arm64" {
		t.Skip("buildVZMachine requires an Apple Silicon macOS host")
	}
	if _, err := vzhelper.FindHelper(); err != nil {
		t.Skip("vee-vz-helper is not installed on this host")
	}
	m := newVZTestManager(t)

	vmDir := m.vmDir("lin")
	if err := os.MkdirAll(filepath.Join(vmDir, "storage"), 0o750); err != nil {
		t.Fatal(err)
	}
	raw := filepath.Join(vmDir, "storage", "disk-os.raw")
	if err := os.WriteFile(raw, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &VMConfig{
		Name: "lin", Backend: "vz", Memory: "2G", CPUs: 2,
		NIC:   NICConfig{Mode: "user"},
		Disks: []DiskConfig{{Path: raw, Format: "raw", Media: "disk"}},
		Vsock: true,
	}
	if _, err := m.buildVZMachine(context.Background(), cfg, false); err != nil {
		t.Fatal(err)
	}

	spec, err := vzhelper.LoadSpec(vmDir)
	if err != nil {
		t.Fatal(err)
	}
	if spec.PlatformName() != vzhelper.PlatformLinux {
		t.Errorf("platform = %q, want %q", spec.PlatformName(), vzhelper.PlatformLinux)
	}
	if spec.EFIVariableStore != vzhelper.EFIVariableStorePath(vmDir) {
		t.Errorf("EFI variable store = %q, want it inside the VM directory", spec.EFIVariableStore)
	}
	if spec.SerialLog != vzhelper.SerialLogPath(vmDir) {
		t.Errorf("serial log = %q, want it inside the VM directory", spec.SerialLog)
	}
	if !spec.Vsock {
		t.Error("vsock did not reach the spec")
	}
	if len(spec.HardwareModel) != 0 || spec.AuxiliaryStorage != "" {
		t.Error("linux spec carries macOS restore artifacts")
	}
	if cfg.NIC.MAC == "" {
		t.Error("no deterministic MAC was assigned")
	}
}

// The direct-kernel branch of buildVZMachine (issue #129) writes a spec that
// boots via VZLinuxBootLoader: kernel/cmdline/initrd from the config, no EFI
// variable store — the two boot methods are mutually exclusive.
func TestBuildVZMachineLinuxSpecDirectKernel(t *testing.T) {
	if !platform.IsMacOS() || platform.HostArch() != "arm64" {
		t.Skip("buildVZMachine requires an Apple Silicon macOS host")
	}
	if _, err := vzhelper.FindHelper(); err != nil {
		t.Skip("vee-vz-helper is not installed on this host")
	}
	m := newVZTestManager(t)

	vmDir := m.vmDir("lin")
	if err := os.MkdirAll(filepath.Join(vmDir, "storage"), 0o750); err != nil {
		t.Fatal(err)
	}
	raw := filepath.Join(vmDir, "storage", "disk-os.raw")
	kernel := filepath.Join(vmDir, "vmlinux")
	initrd := filepath.Join(vmDir, "initrd.img")
	for _, p := range []string{raw, kernel, initrd} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &VMConfig{
		Name: "lin", Backend: "vz", Memory: "2G", CPUs: 2,
		NIC:     NICConfig{Mode: "user"},
		Disks:   []DiskConfig{{Path: raw, Format: "raw", Media: "disk"}},
		Kernel:  kernel,
		Cmdline: "console=hvc0 root=/dev/vda",
		Initrd:  initrd,
	}
	if _, err := m.buildVZMachine(context.Background(), cfg, false); err != nil {
		t.Fatal(err)
	}

	spec, err := vzhelper.LoadSpec(vmDir)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Kernel != kernel || spec.Cmdline != cfg.Cmdline || spec.Initrd != initrd {
		t.Errorf("direct-kernel fields did not reach the spec: %+v", spec)
	}
	if spec.EFIVariableStore != "" {
		t.Errorf("EFI variable store = %q, want empty for a direct-kernel boot", spec.EFIVariableStore)
	}

	t.Run("relative kernel path refused", func(t *testing.T) {
		bad := *cfg
		bad.Kernel = "vmlinux"
		if _, err := m.buildVZMachine(context.Background(), &bad, false); err == nil {
			t.Error("expected an error for a relative kernel path")
		}
	})
	t.Run("cmdline without kernel refused", func(t *testing.T) {
		bad := *cfg
		bad.Kernel = ""
		bad.Initrd = ""
		if _, err := m.buildVZMachine(context.Background(), &bad, false); err == nil {
			t.Error("expected an error for cmdline without kernel")
		}
	})
}

// A recovery start of a direct-kernel Linux guest injects the rescue target
// into the SPEC's cmdline only (issue #134): the spec is rewritten from the
// config on every start, so the injection dies with the boot that asked for
// it — and it must never ride the macOS recovery flag.
func TestBuildVZMachineLinuxRecoveryCmdline(t *testing.T) {
	if !platform.IsMacOS() || platform.HostArch() != "arm64" {
		t.Skip("buildVZMachine requires an Apple Silicon macOS host")
	}
	if _, err := vzhelper.FindHelper(); err != nil {
		t.Skip("vee-vz-helper is not installed on this host")
	}
	m := newVZTestManager(t)

	vmDir := m.vmDir("lin")
	if err := os.MkdirAll(filepath.Join(vmDir, "storage"), 0o750); err != nil {
		t.Fatal(err)
	}
	raw := filepath.Join(vmDir, "storage", "disk-os.raw")
	kernel := filepath.Join(vmDir, "vmlinux")
	for _, p := range []string{raw, kernel} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	const cmdline = "console=hvc0 root=/dev/vda"
	cfg := &VMConfig{
		Name: "lin", Backend: "vz", Memory: "2G", CPUs: 2,
		NIC:     NICConfig{Mode: "user"},
		Disks:   []DiskConfig{{Path: raw, Format: "raw", Media: "disk"}},
		Kernel:  kernel,
		Cmdline: cmdline,
	}
	if _, err := m.buildVZMachine(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}

	spec, err := vzhelper.LoadSpec(vmDir)
	if err != nil {
		t.Fatal(err)
	}
	if want := cmdline + " " + linuxRescueCmdline; spec.Cmdline != want {
		t.Errorf("spec cmdline = %q, want %q", spec.Cmdline, want)
	}
	if spec.Recovery {
		t.Error("linux rescue must ride the cmdline, not the macOS recovery flag")
	}
	if cfg.Cmdline != cmdline {
		t.Errorf("recovery leaked into the config cmdline: %q", cfg.Cmdline)
	}

	// The next normal start rewrites the spec without the injection.
	if _, err := m.buildVZMachine(context.Background(), cfg, false); err != nil {
		t.Fatal(err)
	}
	spec, err = vzhelper.LoadSpec(vmDir)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Cmdline != cmdline {
		t.Errorf("rescue injection survived a normal start: %q", spec.Cmdline)
	}
}

// A recovery start of a macOS guest sets the spec's recovery flag — but only
// when the installed helper is new enough to honour it: an older helper would
// ignore the unknown field and silently boot the guest normally, so the build
// must refuse instead (issue #134).
func TestBuildVZMachineMacOSRecovery(t *testing.T) {
	if !platform.IsMacOS() || platform.HostArch() != "arm64" {
		t.Skip("buildVZMachine requires an Apple Silicon macOS host")
	}
	if _, err := vzhelper.FindHelper(); err != nil {
		t.Skip("vee-vz-helper is not installed on this host")
	}
	m := newVZTestManager(t)

	vmDir := m.vmDir("mac")
	if err := os.MkdirAll(filepath.Join(vmDir, "storage"), 0o750); err != nil {
		t.Fatal(err)
	}
	raw := filepath.Join(vmDir, "storage", "disk-os.raw")
	aux := filepath.Join(vmDir, "aux.img")
	for _, p := range []string{raw, aux} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &VMConfig{
		Name: "mac", Backend: "vz", Memory: "8G", CPUs: 4,
		NIC:   NICConfig{Mode: "user"},
		Disks: []DiskConfig{{Path: raw, Format: "raw", Media: "disk"}},
		MacOS: &MacOSConfig{
			AuxiliaryStorage:  aux,
			HardwareModel:     []byte{1},
			MachineIdentifier: []byte{2},
		},
	}

	t.Run("helper too old", func(t *testing.T) {
		orig := helperProtocol
		helperProtocol = func(context.Context, string) int { return vzhelper.ProtocolRecovery - 1 }
		t.Cleanup(func() { helperProtocol = orig })
		_, err := m.buildVZMachine(context.Background(), cfg, true)
		if err == nil || !strings.Contains(err.Error(), "predates --recovery") {
			t.Errorf("buildVZMachine = %v, want a helper-too-old error", err)
		}
	})

	orig := helperProtocol
	helperProtocol = func(context.Context, string) int { return vzhelper.ProtocolRecovery }
	t.Cleanup(func() { helperProtocol = orig })
	if _, err := m.buildVZMachine(context.Background(), cfg, true); err != nil {
		t.Fatal(err)
	}
	spec, err := vzhelper.LoadSpec(vmDir)
	if err != nil {
		t.Fatal(err)
	}
	if !spec.Recovery {
		t.Error("recovery did not reach the spec")
	}

	// One boot only: the next normal start rewrites the spec without it.
	if _, err := m.buildVZMachine(context.Background(), cfg, false); err != nil {
		t.Fatal(err)
	}
	spec, err = vzhelper.LoadSpec(vmDir)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Recovery {
		t.Error("recovery survived a normal start")
	}
}

// A macos: section only means something to the vz backend; pairing it with
// QEMU must fail at create, not at first start.
func TestCreateRefusesMacOSSectionOnQEMU(t *testing.T) {
	m := newVZTestManager(t)
	cfg := &VMConfig{Name: "mac", Backend: "qemu", MacOS: &MacOSConfig{}}
	if err := m.Create(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "vz") {
		t.Errorf("Create = %v, want a macos-requires-vz error", err)
	}
}

// Direct-kernel boot is wired only for vz Linux guests; other pairings must
// fail at create, not at first start (issue #129).
func TestCreateRefusesDirectKernelPairings(t *testing.T) {
	m := newVZTestManager(t)
	ctx := context.Background()

	tests := []struct {
		name string
		cfg  *VMConfig
	}{
		{"kernel on qemu", &VMConfig{Name: "k", Backend: "qemu", Kernel: "/boot/vmlinux"}},
		{"kernel on a macos guest", &VMConfig{Name: "k", Backend: "vz", Kernel: "/boot/vmlinux", MacOS: &MacOSConfig{}}},
		{"cmdline without kernel", &VMConfig{Name: "k", Backend: "vz", Cmdline: "console=hvc0"}},
		{"initrd without kernel", &VMConfig{Name: "k", Backend: "vz", Initrd: "/boot/initrd.img"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := m.Create(ctx, tt.cfg); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestVZVsockOpError(t *testing.T) {
	// A helper that predates the vsock ops answers with "unknown op"; the
	// user needs to hear "update the helper", not a raw protocol error.
	err := vzVsockOpError(`unknown op "vsock-connect"`)
	for _, want := range []string{"vee-vz-helper", "make vz-helper", "restart the VM"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("unknown-op error %q is missing %q", err, want)
		}
	}

	// Any other helper error must pass through untouched.
	if got := vzVsockOpError("vsock is not enabled in the machine spec").Error(); got != "vsock is not enabled in the machine spec" {
		t.Errorf("passthrough error = %q", got)
	}
}
