//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Benehiko/vee/internal/sshkeys"
	"golang.org/x/crypto/ssh"
)

// veeBin returns the path to the vee binary under test.
// Set VEE_BIN env var to override; defaults to the repo root binary.
func veeBin(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("VEE_BIN"); v != "" {
		return v
	}
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}
	bin := filepath.Join(root, "vee")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("vee binary not found at %s — run 'go build -o vee .' first", bin)
	}
	return bin
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// veeCmd builds an exec.Cmd for the vee binary, with HOME pointed at a
// temporary directory so tests never touch the real ~/.vee.
func veeCmd(t *testing.T, home string, args ...string) *exec.Cmd {
	t.Helper()
	bin := veeBin(t)
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// veePrivKeyPath returns the path to the vee-managed private key for home,
// ensuring the keypair exists (generating it if needed).
func veePrivKeyPath(t *testing.T, home string) string {
	t.Helper()
	_, privKeyPath, err := sshkeys.EnsureVeeKeyPair(home)
	if err != nil {
		t.Fatalf("ensure vee keypair: %v", err)
	}
	return privKeyPath
}

// waitSSHAuth polls addr until a full SSH key-auth handshake succeeds or the
// deadline passes. This is stricter than a TCP probe — it ensures cloud-init
// has finished writing authorized_keys before we proceed.
func waitSSHAuth(t *testing.T, addr, user, privKeyPath string, timeout time.Duration) {
	t.Helper()
	keyBytes, err := os.ReadFile(privKeyPath)
	if err != nil {
		t.Fatalf("read private key: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         5 * time.Second,
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		client, dialErr := ssh.Dial("tcp", addr, cfg)
		if dialErr == nil {
			_ = client.Close()
			return
		}
		t.Logf("waiting for SSH auth at %s: %v", addr, dialErr)
		time.Sleep(10 * time.Second)
	}
	t.Fatalf("SSH key auth at %s did not succeed within %s", addr, timeout)
}

// sshRun opens an SSH session to addr and runs command, returning trimmed stdout+stderr.
func sshRun(t *testing.T, addr, user, privKeyPath, command string) string {
	t.Helper()
	keyBytes, err := os.ReadFile(privKeyPath)
	if err != nil {
		t.Fatalf("read private key: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}

	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatalf("ssh dial %s: %v", addr, err)
	}
	defer func() { _ = client.Close() }()

	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("ssh new session: %v", err)
	}
	defer func() { _ = sess.Close() }()

	out, err := sess.CombinedOutput(command)
	if err != nil {
		t.Fatalf("ssh run %q: %v\noutput: %s", command, err, out)
	}
	return strings.TrimSpace(string(out))
}

// sshRunLenient runs a command over SSH and returns combined output without
// fataling on non-zero exit — useful for diagnostic collection before assertions.
func sshRunLenient(t *testing.T, addr, user, privKeyPath, command string) string {
	t.Helper()
	keyBytes, err := os.ReadFile(privKeyPath)
	if err != nil {
		return fmt.Sprintf("read key error: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return fmt.Sprintf("parse key error: %v", err)
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         10 * time.Second,
	}
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return fmt.Sprintf("dial error: %v", err)
	}
	defer func() { _ = client.Close() }()
	sess, err := client.NewSession()
	if err != nil {
		return fmt.Sprintf("session error: %v", err)
	}
	defer func() { _ = sess.Close() }()
	out, _ := sess.CombinedOutput(command)
	return strings.TrimSpace(string(out))
}

// resolveSSHPort reads the ssh_port field from the VM state.
// It tries the sqlite DB first (daemon mode), then falls back to state.json.
func resolveSSHPort(t *testing.T, home, vmName string) int {
	t.Helper()

	// Try DB first — state is stored in sqlite in daemon mode.
	dbPath := filepath.Join(home, ".vee", "vee.db")
	if _, err := os.Stat(dbPath); err == nil {
		// ssh_port column holds it directly; -noheader suppresses the column name line.
		out, err := exec.Command("sqlite3", "-noheader", dbPath,
			fmt.Sprintf("SELECT ssh_port FROM vm_states WHERE vm_name='%s';", vmName),
		).Output()
		if err == nil {
			portStr := strings.TrimSpace(string(out))
			if portStr != "" && portStr != "0" {
				port := 0
				if _, scanErr := fmt.Sscanf(portStr, "%d", &port); scanErr == nil && port > 0 {
					return port
				}
			}
		}
		// ssh_port column is 0 — try state_json which may embed it.
		out, err = exec.Command("sqlite3", "-noheader", dbPath,
			fmt.Sprintf("SELECT state_json FROM vm_states WHERE vm_name='%s';", vmName),
		).Output()
		if err == nil && len(out) > 0 {
			var state struct {
				SSHPort int `json:"ssh_port"`
			}
			if jsonErr := json.Unmarshal(bytes.TrimSpace(out), &state); jsonErr == nil && state.SSHPort > 0 {
				return state.SSHPort
			}
		}
	}

	// Fallback: flat state.json (file-backed mode, used in older layouts).
	statePath := filepath.Join(home, ".vee", "vms", vmName, "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file %s (also tried DB at %s): %v", statePath, dbPath, err)
	}
	var state struct {
		SSHPort int `json:"ssh_port"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("parse state file: %v", err)
	}
	if state.SSHPort == 0 {
		t.Fatalf("ssh_port is 0 in state file %s", statePath)
	}
	return state.SSHPort
}

// runWithContext runs cmd, killing it if ctx is cancelled.
func runWithContext(ctx context.Context, cmd *exec.Cmd) error {
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return ctx.Err()
	}
}

// TestVMStartInstallSSH is the core e2e scenario:
//  1. vee create with ubuntu devbox template (headless, auto SSH port)
//     — vee auto-injects the vee-managed keypair (~/.vee/ssh/id_ed25519)
//  2. vee start --wait-ready (blocks until cloud-init completes and SSH answers)
//  3. SSH smoke tests: hostname, docker, zsh
//  4. vee stop
//  5. vee delete (deferred cleanup)
func TestVMStartInstallSSH(t *testing.T) {
	if os.Getenv("VEE_E2E") == "" {
		t.Skip("set VEE_E2E=1 to run e2e tests (requires KVM)")
	}

	home := t.TempDir()

	// vee create auto-injects ~/.vee/ssh/id_ed25519 — ensure it exists in our
	// isolated home so we know the path for SSH auth below.
	privKeyPath := veePrivKeyPath(t, home)

	vmName := "e2e-devbox"

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	// Always clean up the VM, even if a step fails.
	t.Cleanup(func() {
		_ = veeCmd(t, home, "stop", vmName).Run()
		_ = veeCmd(t, home, "delete", vmName).Run()
	})

	// Step 1: create — no --ssh-keys needed; vee injects its own key automatically.
	t.Log("creating VM...")
	if err := veeCmd(t, home,
		"create", vmName,
		"--template", "devbox",
		"--distro", "ubuntu",
		"--headless",
		"--memory", "2G",
		"--cpus", "2",
	).Run(); err != nil {
		t.Fatalf("vee create: %v", err)
	}

	// Step 2: start and wait for readiness (cloud-init + SSH port open)
	t.Log("starting VM (waiting up to 15m for cloud-init to complete)...")
	if err := runWithContext(ctx, veeCmd(t, home, "start", vmName, "--wait-ready")); err != nil {
		t.Fatalf("vee start --wait-ready: %v", err)
	}

	sshPort := resolveSSHPort(t, home, vmName)
	sshAddr := fmt.Sprintf("127.0.0.1:%d", sshPort)
	t.Logf("VM SSH address: %s", sshAddr)

	// Wait until the default cloud image user ("ubuntu") can authenticate.
	// The top-level ssh_authorized_keys in user-data is applied to this user
	// immediately on first boot, before any runcmd runs.
	waitSSHAuth(t, sshAddr, "ubuntu", privKeyPath, 10*time.Minute)

	// Step 3: smoke tests — run as ubuntu; wait for cloud-init runcmd to finish
	// before checking tools installed by runcmd (docker, zsh).
	t.Log("running SSH smoke tests...")

	hostname := sshRun(t, sshAddr, "ubuntu", privKeyPath, "hostname")
	if hostname != vmName {
		t.Errorf("hostname: got %q, want %q", hostname, vmName)
	}

	// cloud-init status --wait blocks until all modules complete.
	sshRun(t, sshAddr, "ubuntu", privKeyPath, "sudo cloud-init status --wait")

	dockerOut := sshRun(t, sshAddr, "ubuntu", privKeyPath, "docker --version")
	if !strings.Contains(dockerOut, "Docker") {
		t.Errorf("docker not installed: %q", dockerOut)
	}

	zshPath := sshRun(t, sshAddr, "ubuntu", privKeyPath, "which zsh")
	if zshPath == "" {
		t.Error("zsh not installed")
	}

	// Step 4: stop (cleanup handles delete)
	t.Log("stopping VM...")
	if err := veeCmd(t, home, "stop", vmName).Run(); err != nil {
		t.Fatalf("vee stop: %v", err)
	}
}
