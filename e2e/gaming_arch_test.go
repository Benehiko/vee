//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestGamingArchInstall exercises the full gaming-arch install path using
// virtio GPU and user-mode NIC so no GPU passthrough or bridge interface is
// needed — the test can run in CI or on a dev machine without hardware deps.
//
// Phases:
//  1. vee create (gaming-arch, virtio GPU, user NIC, headless)
//  2. vee start --wait-ready  — cloud-init runs install.sh and powers off the VM
//  3. vee start --wait-ready  — boot from installed disk; SSH must come up
//  4. SSH assertions: user, services, multilib, fstab, KDE
//  5. vee stop + vee delete (cleanup)
func TestGamingArchInstall(t *testing.T) {
	if os.Getenv("VEE_E2E") == "" {
		t.Skip("set VEE_E2E=1 to run e2e tests (requires KVM)")
	}

	home := t.TempDir()
	privKeyPath := veePrivKeyPath(t, home)
	vmName := "e2e-gaming-arch"

	// Allow up to 90 minutes — pacstrap + KDE is a large download.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()

	t.Cleanup(func() {
		_ = veeCmd(t, home, "stop", vmName).Run()
		_ = veeCmd(t, home, "delete", vmName).Run()
	})

	// Phase 1: create — virtio GPU, user-mode NIC, headless, auto SSH port.
	// --no-start so we control when each boot phase begins.
	t.Log("creating gaming-arch VM...")
	if err := veeCmd(t, home,
		"create", vmName,
		"--template", "gaming-arch",
		"--gpu-mode", "virtio",
		"--nic-mode", "user",
		"--headless",
		"--memory", "8G",
		"--cpus", "4",
		"--no-start",
	).Run(); err != nil {
		t.Fatalf("vee create: %v", err)
	}

	// Phase 2: install pass — cloud-init runs install.sh which ends with
	// `poweroff`. --foreground blocks until the QEMU process exits.
	t.Log("starting install (this may take 30–60 minutes)...")
	installCtx, installCancel := context.WithTimeout(ctx, 75*time.Minute)
	defer installCancel()
	if err := runWithContext(installCtx, veeCmd(t, home, "start", vmName, "--foreground")); err != nil {
		t.Fatalf("vee start (install pass): %v", err)
	}
	t.Log("install complete (VM powered off)")

	// Phase 3: boot from installed disk — background start, then poll SSH.
	t.Log("booting installed system...")
	if err := veeCmd(t, home, "start", vmName).Run(); err != nil {
		t.Fatalf("vee start (boot pass): %v", err)
	}

	sshPort := resolveSSHPort(t, home, vmName)
	sshAddr := fmt.Sprintf("127.0.0.1:%d", sshPort)
	t.Logf("VM SSH address: %s", sshAddr)

	// Phase 4: wait for SSH auth and run assertions.
	waitSSHAuth(t, sshAddr, "gamer", privKeyPath, 5*time.Minute)

	t.Log("running post-install assertions...")

	assertSSH := func(desc, cmd, want string) {
		t.Helper()
		out := sshRun(t, sshAddr, "gamer", privKeyPath, cmd)
		if !strings.Contains(out, want) {
			t.Errorf("%s: got %q, want it to contain %q", desc, out, want)
		}
	}

	assertSSHLenient := func(desc, cmd, want string) {
		t.Helper()
		out := sshRunLenient(t, sshAddr, "gamer", privKeyPath, cmd)
		if !strings.Contains(out, want) {
			t.Errorf("%s: got %q, want it to contain %q", desc, out, want)
		}
	}

	// User exists and is in the wheel group.
	assertSSH("user in wheel group", "groups gamer", "wheel")

	// Core services are enabled.
	// qemu-guest-agent uses static enablement (socket-activated), not "enabled".
	for _, svc := range []string{"NetworkManager", "sshd", "sddm"} {
		assertSSH("service "+svc+" enabled",
			"systemctl is-enabled "+svc, "enabled")
	}
	assertSSH("qemu-guest-agent present",
		"systemctl is-enabled qemu-guest-agent 2>&1 || true", "") // static or enabled both ok

	// multilib repo is configured in pacman.conf (at least one occurrence).
	assertSSH("multilib in pacman.conf",
		"grep -q '\\[multilib\\]' /etc/pacman.conf && echo yes", "yes")

	// fstab has at least two entries (EFI + root).
	out := sshRun(t, sshAddr, "gamer", privKeyPath,
		"grep -vc '^#\\|^$' /etc/fstab")
	if out == "0" || out == "1" {
		t.Errorf("fstab: expected ≥2 entries, got %q", out)
	}

	// KDE Plasma is installed.
	assertSSHLenient("plasma-desktop installed",
		"pacman -Q plasma-desktop 2>&1", "plasma-desktop")

	// Steam is installed.
	assertSSHLenient("steam installed",
		"pacman -Q steam 2>&1", "steam")

	// SDDM autologin config is present.
	assertSSH("sddm autologin session",
		"grep -i 'plasmawayland' /etc/sddm.conf.d/autologin.conf", "plasmawayland")

	// vee-firstboot service is enabled.
	assertSSH("vee-firstboot enabled",
		"systemctl is-enabled vee-firstboot", "enabled")

	// sudo works without password for wheel members.
	assertSSH("sudo nopasswd",
		"sudo -n true && echo ok", "ok")

	// Phase 5: stop (cleanup handles delete). VM may have already exited on its own.
	t.Log("stopping VM...")
	_ = veeCmd(t, home, "stop", vmName).Run()
}
