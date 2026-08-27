package templates

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Benehiko/vee/internal/shacrypt"
)

func omarchySeedByName(t *testing.T, name string) string {
	t.Helper()
	files, err := omarchySeedFiles("box", "dev", "hunter2", []string{"ssh-ed25519 AAAA key1", "ssh-rsa BBBB key2"})
	if err != nil {
		t.Fatalf("omarchySeedFiles: %v", err)
	}
	for _, f := range files {
		if f.Name == name {
			return f.Content
		}
	}
	t.Fatalf("seed file %q missing", name)
	return ""
}

// The cidata loader on the ISO requires exactly this file pair to treat the
// drive as an autoinstall source; anything less falls back to the wizard.
func TestOmarchySeedHasRequiredPair(t *testing.T) {
	for _, required := range []string{"user_configuration.json", "user_credentials.json"} {
		if omarchySeedByName(t, required) == "" {
			t.Errorf("%s is empty", required)
		}
	}
}

func TestOmarchySeedCredentials(t *testing.T) {
	var creds struct {
		RootEncPassword string `json:"root_enc_password"`
		Users           []struct {
			EncPassword string `json:"enc_password"`
			Sudo        bool   `json:"sudo"`
			Username    string `json:"username"`
		} `json:"users"`
	}
	if err := json.Unmarshal([]byte(omarchySeedByName(t, "user_credentials.json")), &creds); err != nil {
		t.Fatalf("user_credentials.json is not valid JSON: %v", err)
	}
	if len(creds.Users) != 1 {
		t.Fatalf("want exactly 1 user, got %d", len(creds.Users))
	}
	u := creds.Users[0]
	if u.Username != "dev" || !u.Sudo {
		t.Errorf("user = %+v, want username dev with sudo", u)
	}

	// The hash must be a SHA-512 crypt of the given password: re-derive it
	// from the embedded salt and compare.
	parts := strings.Split(u.EncPassword, "$")
	if len(parts) != 4 || parts[1] != "6" {
		t.Fatalf("enc_password %q is not a $6$ crypt hash", u.EncPassword)
	}
	rehashed, err := shacrypt.Sha512Crypt("hunter2", parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if rehashed != u.EncPassword {
		t.Errorf("enc_password does not verify against the password:\n got  %s\n want %s", u.EncPassword, rehashed)
	}
	if creds.RootEncPassword != u.EncPassword {
		t.Errorf("root_enc_password differs from the user hash")
	}
}

// The disk plan must describe the whole 60G virtio target declaratively:
// non-pre-mounted, /dev/vda, 2GiB ESP + btrfs rest, 1MiB gaps at both ends —
// the same layout Omarchy's wizard partitions interactively.
func TestOmarchySeedDiskPlan(t *testing.T) {
	var cfg struct {
		Hostname   string `json:"hostname"`
		DiskConfig struct {
			ConfigType    string `json:"config_type"`
			Modifications []struct {
				Device     string `json:"device"`
				Wipe       bool   `json:"wipe"`
				Partitions []struct {
					FSType string `json:"fs_type"`
					Size   struct {
						Value int64 `json:"value"`
					} `json:"size"`
					Start struct {
						Value int64 `json:"value"`
					} `json:"start"`
				} `json:"partitions"`
			} `json:"device_modifications"`
		} `json:"disk_config"`
		OmarchyInstall struct {
			Mode string `json:"mode"`
		} `json:"omarchy_install"`
	}
	if err := json.Unmarshal([]byte(omarchySeedByName(t, "user_configuration.json")), &cfg); err != nil {
		t.Fatalf("user_configuration.json is not valid JSON: %v", err)
	}

	if cfg.Hostname != "box" {
		t.Errorf("hostname = %q, want box", cfg.Hostname)
	}
	if cfg.OmarchyInstall.Mode != "full_disk" {
		t.Errorf("omarchy_install.mode = %q, want full_disk", cfg.OmarchyInstall.Mode)
	}
	if cfg.DiskConfig.ConfigType != "default_layout" {
		t.Errorf("config_type = %q, want default_layout (pre_mounted would skip partitioning)", cfg.DiskConfig.ConfigType)
	}
	if len(cfg.DiskConfig.Modifications) != 1 {
		t.Fatalf("want 1 device modification, got %d", len(cfg.DiskConfig.Modifications))
	}
	mod := cfg.DiskConfig.Modifications[0]
	if mod.Device != "/dev/vda" || !mod.Wipe {
		t.Errorf("device = %q wipe = %t, want /dev/vda with wipe", mod.Device, mod.Wipe)
	}
	if len(mod.Partitions) != 2 {
		t.Fatalf("want 2 partitions, got %d", len(mod.Partitions))
	}

	const mib, gib = int64(1) << 20, int64(1) << 30
	esp, root := mod.Partitions[0], mod.Partitions[1]
	if esp.FSType != "fat32" || esp.Start.Value != mib || esp.Size.Value != 2*gib {
		t.Errorf("ESP = %s start %d size %d, want fat32 at 1MiB sized 2GiB", esp.FSType, esp.Start.Value, esp.Size.Value)
	}
	wantRootSize := int64(omarchyDiskBytes) - (mib + 2*gib) - mib
	if root.FSType != "btrfs" || root.Start.Value != mib+2*gib || root.Size.Value != wantRootSize {
		t.Errorf("root = %s start %d size %d, want btrfs at %d sized %d",
			root.FSType, root.Start.Value, root.Size.Value, mib+2*gib, wantRootSize)
	}
}

// Emulation is opt-in for every image that does not support the host arch:
// a cross-arch image must be refused with the --emulate hint rather than
// silently dropped into a TCG guest, the flag must lift the refusal (and pin
// VMConfig.Arch to the image's arch), and native images never need the flag.
func TestGuestArchGate(t *testing.T) {
	if arch, err := guestArchOn("x86_64", "x86_64", "omarchy", false); err != nil || arch != "" {
		t.Errorf("native image gated: arch=%q err=%v", arch, err)
	}
	if arch, err := guestArchOn("aarch64", "x86_64", "omarchy", true); err != nil || arch != "x86_64" {
		t.Errorf("cross-arch with --emulate: arch=%q err=%v, want x86_64 and no error", arch, err)
	}
	_, err := guestArchOn("aarch64", "x86_64", "omarchy", false)
	if err == nil {
		t.Fatal("cross-arch image accepted without --emulate")
	}
	if !strings.Contains(err.Error(), "--emulate") {
		t.Errorf("refusal does not hint at --emulate: %v", err)
	}
}

// authorized_keys drives SSH access to the installed system (the installer
// enables sshd when it is present), so every key must land there verbatim.
func TestOmarchySeedAuthorizedKeys(t *testing.T) {
	got := omarchySeedByName(t, "authorized_keys")
	want := "ssh-ed25519 AAAA key1\nssh-rsa BBBB key2\n"
	if got != want {
		t.Errorf("authorized_keys = %q, want %q", got, want)
	}
}
