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

	// Phase 4: wait for SSH auth and run health checks.
	waitSSHAuth(t, sshAddr, "gamer", privKeyPath, 5*time.Minute)

	t.Log("running post-install health checks via vee-check...")

	// Safety net: verify the script is present before relying on it.
	scriptPresent := sshRunLenient(t, sshAddr, "gamer", privKeyPath,
		"test -x /usr/local/bin/vee-check && echo ok")
	if !strings.Contains(scriptPresent, "ok") {
		t.Fatalf("vee-check script missing or not executable on installed VM")
	}

	checks := sshRunHealthCheck(t, sshAddr, "gamer", privKeyPath)
	if len(checks) == 0 {
		t.Fatal("vee-check returned no checks — script may have failed to produce output")
	}

	allPassed := true
	for _, c := range checks {
		if !c.OK {
			t.Errorf("health check FAILED: %s — %s", c.Name, c.Detail)
			allPassed = false
		}
	}
	if allPassed {
		t.Logf("all %d health checks passed", len(checks))
	}

	// Phase 5: stop and restart — verifies the VM boots from disk cleanly on a
	// second cycle (catches boot-order / PXE regression).
	t.Log("stopping VM for restart cycle...")
	if err := veeCmd(t, home, "stop", vmName).Run(); err != nil {
		t.Fatalf("vee stop: %v", err)
	}

	t.Log("restarting VM (second boot cycle)...")
	if err := veeCmd(t, home, "start", vmName).Run(); err != nil {
		t.Fatalf("vee start (second boot): %v", err)
	}

	waitSSHAuth(t, sshAddr, "gamer", privKeyPath, 5*time.Minute)
	t.Log("second boot cycle: SSH is up — boot order is correct")

	// Phase 6: stop (cleanup handles delete).
	t.Log("stopping VM...")
	_ = veeCmd(t, home, "stop", vmName).Run()
}
