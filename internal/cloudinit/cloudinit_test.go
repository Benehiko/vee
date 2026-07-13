package cloudinit_test

import (
	"os"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/Benehiko/vee/internal/cloudinit"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func mustUserData(t *testing.T, cfg *cloudinit.Config) string {
	t.Helper()
	ud, err := cloudinit.RenderUserData(cfg)
	if err != nil {
		t.Fatalf("RenderUserData: %v", err)
	}
	return ud
}

func TestUserDataHostname(t *testing.T) {
	ud := mustUserData(t, &cloudinit.Config{Hostname: "myvm"})
	if !strings.Contains(ud, "hostname: myvm") {
		t.Errorf("hostname missing: %q", ud)
	}
}

func TestUserDataSSHKeysNoCustomUser(t *testing.T) {
	ud := mustUserData(t, &cloudinit.Config{
		SSHKeys: []string{"ssh-ed25519 AAAA test@host"},
	})
	if !strings.Contains(ud, "ssh_authorized_keys:") {
		t.Errorf("ssh_authorized_keys missing: %q", ud)
	}
	if strings.Contains(ud, "users:") {
		t.Errorf("unexpected users block: %q", ud)
	}
}

func TestUserDataCustomUser(t *testing.T) {
	ud := mustUserData(t, &cloudinit.Config{
		User:        "vee",
		DefaultUser: "ubuntu",
		SSHKeys:     []string{"ssh-ed25519 AAAA test@host"},
	})
	if !strings.Contains(ud, "users:") {
		t.Errorf("users block missing: %q", ud)
	}
	if !strings.Contains(ud, "name: vee") {
		t.Errorf("custom user missing: %q", ud)
	}
	if !strings.Contains(ud, "name: ubuntu") {
		t.Errorf("default user missing: %q", ud)
	}
}

func TestUserDataPackages(t *testing.T) {
	ud := mustUserData(t, &cloudinit.Config{
		Packages: []string{"git", "curl"},
	})
	if !strings.Contains(ud, "packages:") {
		t.Errorf("packages section missing: %q", ud)
	}
	if !strings.Contains(ud, "- git") {
		t.Errorf("git package missing: %q", ud)
	}
	if !strings.Contains(ud, "package_update: true") {
		t.Errorf("package_update missing: %q", ud)
	}
}

func TestUserDataRunCmds(t *testing.T) {
	ud := mustUserData(t, &cloudinit.Config{
		RunCmds: []string{"echo hello", "systemctl enable foo"},
	})
	if !strings.Contains(ud, "runcmd:") {
		t.Errorf("runcmd section missing: %q", ud)
	}
	if !strings.Contains(ud, "echo hello") {
		t.Errorf("run command missing: %q", ud)
	}
}

func TestUserDataPassword(t *testing.T) {
	ud := mustUserData(t, &cloudinit.Config{
		User:        "gamer",
		DefaultUser: "ubuntu",
		Password:    "hunter2",
		RunCmds:     []string{"echo trailing"},
	})
	if !strings.Contains(ud, "echo 'gamer:hunter2' | chpasswd") {
		t.Errorf("custom user chpasswd missing: %q", ud)
	}
	if !strings.Contains(ud, "echo 'ubuntu:hunter2' | chpasswd") {
		t.Errorf("default user chpasswd missing: %q", ud)
	}
	// Password runcmds must come before the template's own runcmds so the
	// account is usable from the console immediately.
	gamerIdx := strings.Index(ud, "echo 'gamer:hunter2'")
	tailIdx := strings.Index(ud, "echo trailing")
	if gamerIdx < 0 || tailIdx < 0 || gamerIdx > tailIdx {
		t.Errorf("password runcmd not prepended: %q", ud)
	}
}

func TestUserDataPasswordSameUserAndDefault(t *testing.T) {
	ud := mustUserData(t, &cloudinit.Config{
		User:        "ubuntu",
		DefaultUser: "ubuntu",
		Password:    "pw",
	})
	if strings.Count(ud, "chpasswd") != 1 {
		t.Errorf("expected single chpasswd line when user==default: %q", ud)
	}
}

func TestUserDataPasswordQuoteEscape(t *testing.T) {
	ud := mustUserData(t, &cloudinit.Config{
		User:     "alice",
		Password: "it's",
	})
	if !strings.Contains(ud, `echo 'alice:it'\''s' | chpasswd`) {
		t.Errorf("single-quote escape wrong: %q", ud)
	}
}

func TestUserDataNoPasswordNoChpasswd(t *testing.T) {
	ud := mustUserData(t, &cloudinit.Config{
		User: "vee",
	})
	if strings.Contains(ud, "chpasswd") {
		t.Errorf("unexpected chpasswd when password unset: %q", ud)
	}
}

func TestMetaDataHostname(t *testing.T) {
	md := cloudinit.RenderMetaData(&cloudinit.Config{Hostname: "testvm"})
	if !strings.Contains(md, "local-hostname: testvm") {
		t.Errorf("hostname missing: %q", md)
	}
	if !strings.Contains(md, "instance-id: testvm") {
		t.Errorf("instance-id missing: %q", md)
	}
}

func TestMetaDataDefaultHostname(t *testing.T) {
	md := cloudinit.RenderMetaData(&cloudinit.Config{})
	if !strings.Contains(md, "local-hostname: vee-vm") {
		t.Errorf("default hostname missing: %q", md)
	}
}

// TestGenerateISO builds a real cidata seed with whichever ISO tool is present
// (xorriso/genisoimage on Linux, hdiutil on macOS) and validates it is a
// ISO9660 image labelled "cidata" — the two things cloud-init's NoCloud
// datasource keys on. Skips when no tool is available (e.g. a bare CI runner).
func TestGenerateISO(t *testing.T) {
	dir := t.TempDir()
	isoPath, err := cloudinit.Generate(dir, &cloudinit.Config{Hostname: "veetest"})
	if err != nil {
		if strings.Contains(err.Error(), "no ISO build tool found") {
			t.Skip("no ISO build tool available on this host")
		}
		t.Fatalf("Generate: %v", err)
	}

	data, err := os.ReadFile(isoPath) //nolint:gosec // isoPath is returned by Generate for a path under the test's own TempDir.
	if err != nil {
		t.Fatalf("read iso: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("cidata.iso is empty")
	}

	// The ISO9660 primary volume descriptor starts at sector 16 with "CD001".
	const magicOff = 16*2048 + 1
	if len(data) < magicOff+5 || string(data[magicOff:magicOff+5]) != "CD001" {
		t.Fatalf("missing ISO9660 CD001 signature at offset %d", magicOff)
	}
	// The volume identifier must read as "cidata" (case-insensitive: xorriso
	// upcases it, hdiutil keeps it lowercase; cloud-init accepts either).
	if !strings.Contains(strings.ToLower(string(data)), "cidata") {
		t.Fatal("cidata volume label not found in ISO")
	}
}
