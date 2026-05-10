package cloudinit_test

import (
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
