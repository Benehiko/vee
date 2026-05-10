//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Benehiko/vee/internal/templates"
)

// TestDockerTemplate verifies the docker VM template end-to-end:
//
//  1. Create an Alpine+Docker VM via the docker template.
//  2. Start it and wait for SSH auth (cloud-init complete).
//  3. Assert docker daemon is reachable on tcp://localhost:2375 from the host.
//  4. Assert `docker info` succeeds via the TCP socket.
//  5. Assert `docker run --rm hello-world` succeeds inside the VM (via SSH).
//  6. Stop and delete the VM.
//
// Requires VEE_E2E=1 and KVM access. Timeout: 20 minutes.
func TestDockerTemplate(t *testing.T) {
	if os.Getenv("VEE_E2E") == "" {
		t.Skip("set VEE_E2E=1 to run e2e tests (requires KVM)")
	}

	home := t.TempDir()
	privKeyPath := veePrivKeyPath(t, home)

	vmName := "e2e-docker"

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	t.Cleanup(func() {
		_ = veeCmd(t, home, "stop", vmName).Run()
		_ = veeCmd(t, home, "delete", vmName).Run()
	})

	// Step 1: create the docker VM (no --distro needed — template forces Alpine).
	t.Log("creating docker VM...")
	if err := veeCmd(t, home,
		"create", vmName,
		"--template", "docker",
		"--no-start",
	).Run(); err != nil {
		t.Fatalf("vee create: %v", err)
	}

	// Step 2: start and wait for readiness.
	t.Log("starting VM (waiting up to 15m for cloud-init to complete)...")
	if err := runWithContext(ctx, veeCmd(t, home, "start", vmName)); err != nil {
		t.Fatalf("vee start: %v", err)
	}

	sshPort := resolveSSHPort(t, home, vmName)
	sshAddr := fmt.Sprintf("127.0.0.1:%d", sshPort)
	t.Logf("VM SSH address: %s", sshAddr)

	// Alpine default cloud-init user is "alpine".
	const sshUser = "alpine"
	waitSSHAuth(t, sshAddr, sshUser, privKeyPath, 12*time.Minute)

	// Wait for cloud-init to fully complete before checking Docker.
	// Alpine uses OpenRC so there's no cloud-init status --wait; poll instead.
	sshRun(t, sshAddr, sshUser, privKeyPath,
		"until rc-service docker status 2>/dev/null | grep -q started; do sleep 5; done")

	// Step 3: assert Docker TCP port is reachable from the host.
	dockerPort := templates.DockerTCPPort
	t.Logf("probing Docker TCP on localhost:%d", dockerPort)
	if err := waitTCPOpen(fmt.Sprintf("127.0.0.1:%d", dockerPort), 2*time.Minute); err != nil {
		t.Fatalf("docker TCP port not open: %v", err)
	}

	// Step 4: assert docker info succeeds via the host-side TCP socket.
	t.Log("checking docker info via TCP...")
	dockerHost := fmt.Sprintf("tcp://127.0.0.1:%d", dockerPort)
	infoURL := fmt.Sprintf("http://127.0.0.1:%d/info", dockerPort)
	req, err := http.NewRequestWithContext(ctx, "GET", infoURL, nil)
	if err != nil {
		t.Fatalf("build /info request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", infoURL, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("docker /info: got status %d, want 200", resp.StatusCode)
	}
	t.Logf("DOCKER_HOST=%s docker info: HTTP %d", dockerHost, resp.StatusCode)

	// Step 5: run hello-world inside the VM via SSH to exercise the full stack.
	t.Log("running hello-world container inside VM...")
	out := sshRun(t, sshAddr, sshUser, privKeyPath,
		"docker run --rm hello-world 2>&1 || true")
	if !strings.Contains(out, "Hello from Docker") {
		t.Errorf("hello-world did not produce expected output:\n%s", out)
	}

	// Step 6: stop (cleanup handles delete).
	t.Log("stopping VM...")
	if err := veeCmd(t, home, "stop", vmName).Run(); err != nil {
		t.Fatalf("vee stop: %v", err)
	}
}

// waitTCPOpen polls addr until a TCP connection succeeds or timeout elapses.
func waitTCPOpen(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("TCP %s not reachable after %s", addr, timeout)
}
