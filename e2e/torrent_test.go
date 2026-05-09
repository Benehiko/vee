//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Benehiko/vee/provider"
	"github.com/Benehiko/vee/templates"
	"github.com/Benehiko/vee/vm"
)

// TestVMTorrentWireGuard spins up two VMs:
//  1. wg-server: headless Ubuntu running a WireGuard server (10.99.0.1)
//  2. torrent-box: torrent template with WireGuard client (10.99.0.2)
//     and two virtiofs mounts (movies, shows)
//
// Assertions (via SSH into torrent-box):
//   - /movies and /shows are mounted (virtiofs)
//   - wg0 interface is up with 10.99.0.2 assigned
//   - can ping the WireGuard server tunnel IP (10.99.0.1)
//   - qbittorrent-nox service is active
//
// Requires VEE_E2E=1 and KVM access.
func TestVMTorrentWireGuard(t *testing.T) {
	if os.Getenv("VEE_E2E") == "" {
		t.Skip("set VEE_E2E=1 to run e2e tests (requires KVM)")
	}

	home := t.TempDir()
	privKeyPath := veePrivKeyPath(t, home)
	pubKeyBytes, err := os.ReadFile(privKeyPath + ".pub")
	if err != nil {
		t.Fatalf("read public key: %v", err)
	}
	sshPubKey := strings.TrimSpace(string(pubKeyBytes))

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()

	// Build a provider so we can call templates directly (avoids re-implementing
	// cloud-init generation in the test). The provider uses HOME=home so all
	// artifacts land in the temp dir.
	prov, err := providerWithHome(t, home)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	mgr := vm.NewManager(prov)

	moviesDir := t.TempDir()
	showsDir := t.TempDir()

	const (
		wgServerName  = "e2e-wg-server"
		torrentVMName = "e2e-torrent"
		wgHostPort    = 51900 // host UDP port → wg-server VM :51820
	)

	t.Cleanup(func() {
		_ = veeCmd(t, home, "stop", wgServerName).Run()
		_ = veeCmd(t, home, "stop", torrentVMName).Run()
		_ = veeCmd(t, home, "delete", wgServerName).Run()
		_ = veeCmd(t, home, "delete", torrentVMName).Run()
	})

	// --- Step 1: WireGuard server VM ---
	t.Log("creating WireGuard server VM...")
	wgVMCfg, wgServerCfg, err := templates.NewWGServerVMConfig(ctx, prov, wgServerName, []string{sshPubKey}, wgHostPort)
	if err != nil {
		t.Fatalf("NewWGServerVMConfig: %v", err)
	}
	if err := mgr.Create(ctx, wgVMCfg); err != nil {
		t.Fatalf("create wg-server VM: %v", err)
	}

	t.Log("starting WireGuard server VM...")
	if err := mgr.Start(ctx, wgServerName, false); err != nil {
		t.Fatalf("start wg-server VM: %v", err)
	}
	t.Log("waiting for WireGuard server VM to be ready...")
	if err := mgr.WaitReady(ctx, wgServerName, 15*time.Minute); err != nil {
		t.Fatalf("wg-server not ready: %v", err)
	}

	wgSSHPort := resolveSSHPort(t, home, wgServerName)
	wgSSH := fmt.Sprintf("127.0.0.1:%d", wgSSHPort)
	waitSSHAuth(t, wgSSH, "ubuntu", privKeyPath, 5*time.Minute)
	ciStatus := sshRunLenient(t, wgSSH, "ubuntu", privKeyPath, "sudo cloud-init status --wait 2>&1")
	if strings.Contains(ciStatus, "status: error") {
		ciDetail := sshRunLenient(t, wgSSH, "ubuntu", privKeyPath,
			"sudo cloud-init status --long 2>&1; echo '---OUTPUT LOG---'; sudo cat /var/log/cloud-init-output.log 2>&1 | tail -80")
		t.Fatalf("wg-server cloud-init failed:\n%s\n\n%s", ciStatus, ciDetail)
	}
	t.Log("WireGuard server VM ready")

	// --- Step 2: torrent VM with WireGuard client ---
	t.Log("creating torrent VM...")
	clientWGConf := templates.ClientWireGuardConfig(wgServerCfg)
	torrentCfg, err := templates.NewTorrentConfig(ctx, prov, torrentVMName,
		[]string{sshPubKey},
		[]templates.ShareMount{
			{HostDir: moviesDir, GuestPath: "/movies"},
			{HostDir: showsDir, GuestPath: "/shows"},
		},
		nil, // no NordVPN
		clientWGConf,
		"wireguard",
		0,
	)
	if err != nil {
		t.Fatalf("NewTorrentConfig: %v", err)
	}
	if err := mgr.Create(ctx, torrentCfg); err != nil {
		t.Fatalf("create torrent VM: %v", err)
	}

	t.Log("starting torrent VM...")
	if err := mgr.Start(ctx, torrentVMName, false); err != nil {
		t.Fatalf("start torrent VM: %v", err)
	}
	t.Log("waiting for torrent VM to be ready (cloud-init: WireGuard + qbittorrent)...")
	if err := mgr.WaitReady(ctx, torrentVMName, 20*time.Minute); err != nil {
		// Check if QEMU process is still alive.
		torrentState, _ := vm.LoadState(filepath.Join(home, ".vee", "vms"), torrentVMName)
		if torrentState != nil {
			_, pidErr := os.FindProcess(torrentState.PID)
			t.Logf("torrent VM QEMU PID=%d alive=%v", torrentState.PID, pidErr == nil)
		}
		// Try SSH for cloud-init diag.
		torrentSSHPortDiag := resolveSSHPort(t, home, torrentVMName)
		diagSSH := fmt.Sprintf("127.0.0.1:%d", torrentSSHPortDiag)
		diag := sshRunLenient(t, diagSSH, "ubuntu", privKeyPath,
			"sudo cloud-init status --long 2>&1; echo '---OUTPUT---'; sudo cat /var/log/cloud-init-output.log 2>&1 | tail -60; echo '---VIRTIOFS---'; dmesg | grep -i virtiofs | tail -10")
		t.Fatalf("torrent VM not ready: %v\ndiag:\n%s", err, diag)
	}

	torrentSSHPort := resolveSSHPort(t, home, torrentVMName)
	torrentSSH := fmt.Sprintf("127.0.0.1:%d", torrentSSHPort)
	waitSSHAuth(t, torrentSSH, "ubuntu", privKeyPath, 15*time.Minute)
	torrentCIStatus := sshRunLenient(t, torrentSSH, "ubuntu", privKeyPath, "sudo cloud-init status --wait 2>&1")
	if strings.Contains(torrentCIStatus, "status: error") {
		ciDetail := sshRunLenient(t, torrentSSH, "ubuntu", privKeyPath,
			"sudo cloud-init status --long 2>&1; echo '---OUTPUT LOG---'; sudo cat /var/log/cloud-init-output.log 2>&1 | tail -80")
		t.Fatalf("torrent cloud-init failed:\n%s\n\n%s", torrentCIStatus, ciDetail)
	}
	t.Log("torrent VM ready")

	// --- Step 3: assertions ---
	t.Log("asserting virtiofs mounts...")
	sshRun(t, torrentSSH, "ubuntu", privKeyPath, "mountpoint -q /movies")
	sshRun(t, torrentSSH, "ubuntu", privKeyPath, "mountpoint -q /shows")

	t.Log("asserting WireGuard tunnel...")
	wg0Addr := sshRun(t, torrentSSH, "ubuntu", privKeyPath,
		"ip -4 addr show wg0 | grep -oP '(?<=inet )\\S+'")
	if !strings.HasPrefix(wg0Addr, templates.WGClientTunnelIP) {
		t.Errorf("wg0 address: got %q, want prefix %s", wg0Addr, templates.WGClientTunnelIP)
	}
	t.Logf("wg0 address: %s", wg0Addr)

	pingOut := sshRun(t, torrentSSH, "ubuntu", privKeyPath,
		fmt.Sprintf("ping -c 3 -W 5 %s && echo OK", templates.WGServerTunnelIP))
	if !strings.Contains(pingOut, "OK") {
		t.Errorf("ping to WireGuard server (%s) failed: %s", templates.WGServerTunnelIP, pingOut)
	}

	t.Log("asserting qbittorrent-nox service...")
	qbtStatus := sshRun(t, torrentSSH, "ubuntu", privKeyPath,
		"systemctl is-active qbittorrent-nox@vee || true")
	if qbtStatus != "active" {
		t.Errorf("qbittorrent-nox@vee: got %q, want active", qbtStatus)
	}

	// --- Step 4: stop both VMs ---
	t.Log("stopping VMs...")
	if err := veeCmd(t, home, "stop", torrentVMName).Run(); err != nil {
		t.Errorf("stop torrent VM: %v", err)
	}
	if err := veeCmd(t, home, "stop", wgServerName).Run(); err != nil {
		t.Errorf("stop wg-server VM: %v", err)
	}
}

// providerWithHome creates a provider.Provider rooted at the given home dir.
// It wires up the test home so that expensive first-run operations (virtiofsd
// build, image download) are skipped by reusing artifacts from the real home.
func providerWithHome(t *testing.T, home string) (provider.Provider, error) {
	t.Helper()

	realHome, _ := os.UserHomeDir()

	// Symlink the real iso cache so image downloads are skipped.
	realISODir := filepath.Join(realHome, ".vee", "iso")
	testISODir := filepath.Join(home, ".vee", "iso")
	if _, err := os.Stat(realISODir); err == nil {
		if err := os.MkdirAll(filepath.Dir(testISODir), 0o755); err == nil {
			_ = os.Symlink(realISODir, testISODir)
		}
	}

	// Copy virtiofsd binary so EnsureVirtiofsd returns immediately.
	src := filepath.Join(realHome, ".vee", "bin", "virtiofsd")
	if _, err := os.Stat(src); err != nil {
		if p, err2 := exec.LookPath("virtiofsd"); err2 == nil {
			src = p
		} else {
			src = ""
		}
	}
	if src != "" {
		dstDir := filepath.Join(home, ".vee", "bin")
		if err := os.MkdirAll(dstDir, 0o755); err == nil {
			_ = copyFileExec(src, filepath.Join(dstDir, "virtiofsd"))
		}
	}

	t.Setenv("HOME", home)
	return provider.NewProvider()
}

func copyFileExec(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
