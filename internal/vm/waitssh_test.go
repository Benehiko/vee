package vm

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startTestSSHD runs a minimal loopback sshd accepting any public key. Each
// exec request is recorded on cmds and answered with exit status 0.
func startTestSSHD(t *testing.T, hostSigner ssh.Signer, cmds chan<- string) net.Listener {
	t.Helper()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(hostSigner)

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer func() { _ = sc.Close() }()
				go ssh.DiscardRequests(reqs)
				for newCh := range chans {
					if newCh.ChannelType() != "session" {
						_ = newCh.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					ch, chReqs, err := newCh.Accept()
					if err != nil {
						continue
					}
					go func() {
						for req := range chReqs {
							if req.Type != "exec" {
								_ = req.Reply(false, nil)
								continue
							}
							// exec payload: uint32 length + command
							cmd := ""
							if len(req.Payload) >= 4 {
								cmd = string(req.Payload[4:])
							}
							select {
							case cmds <- cmd:
							default:
							}
							_ = req.Reply(true, nil)
							_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
							_ = ch.Close()
						}
					}()
				}
			}()
		}
	}()
	return ln
}

// setupWaitVM writes a fake home with a vee key, plus a VM config/state whose
// SSH port points at the test sshd. Returns the manager.
func setupWaitVM(t *testing.T, template string, sshPort int) *Manager {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".vee", "ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519"), pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newTestManager(t)
	cfg := &VMConfig{Name: "waitvm", Template: template, SSHUser: "tester"}
	if err := m.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	state := &VMState{Running: true, PID: os.Getpid(), SSHPort: sshPort}
	if err := m.SaveState("waitvm", state); err != nil {
		t.Fatal(err)
	}
	return m
}

func testHostSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestWaitSSHReady(t *testing.T) {
	tests := []struct {
		name      string
		template  string
		wantProbe string
	}{
		{name: "posix guest probes true", template: "devbox", wantProbe: "true"},
		{name: "windows guest probes ver", template: "windows", wantProbe: "ver"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmds := make(chan string, 4)
			ln := startTestSSHD(t, testHostSigner(t), cmds)
			port := ln.Addr().(*net.TCPAddr).Port
			m := setupWaitVM(t, tt.template, port)

			if err := m.WaitSSHReady(t.Context(), "waitvm", 30*time.Second, false); err != nil {
				t.Fatalf("WaitSSHReady: %v", err)
			}
			select {
			case got := <-cmds:
				if got != tt.wantProbe {
					t.Errorf("probe command = %q, want %q", got, tt.wantProbe)
				}
			default:
				t.Error("sshd saw no exec request")
			}
		})
	}
}

func TestWaitSSHReadyTimeout(t *testing.T) {
	// A port nothing listens on: the wait must give up at the deadline with a
	// timeout error, not hang.
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	m := setupWaitVM(t, "devbox", port)
	err = m.WaitSSHReady(t.Context(), "waitvm", 1*time.Second, false)
	if err == nil {
		t.Fatal("want timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "not SSH-ready after") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAuthProbeSpec(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *VMConfig
		wantUser string
		wantCmd  string
		wantOK   bool
	}{
		{name: "nil config", cfg: nil},
		{name: "provisioned posix guest", cfg: &VMConfig{Template: "devbox", SSHUser: "dev"}, wantUser: "dev", wantCmd: "true", wantOK: true},
		{name: "windows guest", cfg: &VMConfig{Template: "windows", SSHUser: "vee"}, wantUser: "vee", wantCmd: "ver", wantOK: true},
		{name: "truenas stays on floor", cfg: &VMConfig{Template: "truenas", SSHUser: "admin"}},
		{name: "no account stays on floor", cfg: &VMConfig{Template: "ubuntu-server"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, cmd, ok := authProbeSpec(tt.cfg)
			if user != tt.wantUser || cmd != tt.wantCmd || ok != tt.wantOK {
				t.Errorf("authProbeSpec() = (%q, %q, %v), want (%q, %q, %v)", user, cmd, ok, tt.wantUser, tt.wantCmd, tt.wantOK)
			}
		})
	}
}

// startAcceptCloseListener accepts TCP connections and closes them straight
// away: the port is reachable — the old readiness floor — but no sshd answers.
func startAcceptCloseListener(t *testing.T) int {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestWaitReadyAuthTier(t *testing.T) {
	t.Run("provisioned guest needs the authenticated round-trip", func(t *testing.T) {
		// Port accepts but nothing serves SSH: the old floor would have
		// flipped ready; the auth tier must keep waiting until timeout.
		port := startAcceptCloseListener(t)
		m := setupWaitVM(t, "devbox", port)
		err := m.WaitReady(t.Context(), "waitvm", 6*time.Second)
		if err == nil || !strings.Contains(err.Error(), "authenticated SSH probe") {
			t.Errorf("want authenticated-probe timeout, got %v", err)
		}
	})

	t.Run("provisioned guest ready on auth success", func(t *testing.T) {
		cmds := make(chan string, 4)
		ln := startTestSSHD(t, testHostSigner(t), cmds)
		port := ln.Addr().(*net.TCPAddr).Port
		m := setupWaitVM(t, "devbox", port)
		if err := m.WaitReady(t.Context(), "waitvm", 30*time.Second); err != nil {
			t.Fatalf("WaitReady: %v", err)
		}
	})

	t.Run("keyless guest keeps the reachability floor", func(t *testing.T) {
		port := startAcceptCloseListener(t)
		m := setupWaitVM(t, "ubuntu-server", port)
		cfg, err := m.LoadConfig("waitvm")
		if err != nil {
			t.Fatal(err)
		}
		cfg.SSHUser = "" // imported-disk shape: no account recorded
		if err := m.SaveConfig(cfg); err != nil {
			t.Fatal(err)
		}
		if err := m.WaitReady(t.Context(), "waitvm", 10*time.Second); err != nil {
			t.Fatalf("WaitReady: %v", err)
		}
	})
}

func TestWaitSSHReadyNotRunning(t *testing.T) {
	m := setupWaitVM(t, "devbox", 1)
	if err := m.SaveState("waitvm", &VMState{Running: false}); err != nil {
		t.Fatal(err)
	}
	err := m.WaitSSHReady(t.Context(), "waitvm", time.Second, false)
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("want not-running error, got %v", err)
	}
}
