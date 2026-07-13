package qemu_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Benehiko/vee/internal/qemu"
)

// qgaReadLine reads one newline-terminated line from conn.
func qgaReadLine(conn net.Conn) string {
	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	return strings.TrimRight(string(buf[:n]), "\n")
}

func TestQGADial(t *testing.T) {
	sockPath := filepath.Join(shortSockDir(t), "qga.sock")
	cleanup := mockSocketServer(t, sockPath, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
	})
	defer cleanup()

	client, err := qemu.NewQGAClient(context.Background(), sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("NewQGAClient: %v", err)
	}
	defer func() { _ = client.Close() }()
}

func TestQGAGuestPing(t *testing.T) {
	sockPath := filepath.Join(shortSockDir(t), "qga.sock")
	cleanup := mockSocketServer(t, sockPath, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		line := qgaReadLine(conn)
		if !strings.Contains(line, "guest-ping") {
			t.Errorf("expected guest-ping request, got: %s", line)
		}
		_, _ = fmt.Fprintf(conn, `{"return":{}}`+"\n")
	})
	defer cleanup()

	client, err := qemu.NewQGAClient(context.Background(), sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("NewQGAClient: %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.GuestPing(); err != nil {
		t.Fatalf("GuestPing: %v", err)
	}
}

func TestQGAGuestExec(t *testing.T) {
	sockPath := filepath.Join(shortSockDir(t), "qga.sock")
	cleanup := mockSocketServer(t, sockPath, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		line := qgaReadLine(conn)
		if !strings.Contains(line, "guest-exec") {
			t.Errorf("expected guest-exec request, got: %s", line)
		}
		if !strings.Contains(line, "/bin/echo") {
			t.Errorf("expected /bin/echo in request, got: %s", line)
		}
		_, _ = fmt.Fprintf(conn, `{"return":{"pid":42}}`+"\n")
	})
	defer cleanup()

	client, err := qemu.NewQGAClient(context.Background(), sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("NewQGAClient: %v", err)
	}
	defer func() { _ = client.Close() }()

	pid, err := client.GuestExec("/bin/echo", []string{"hello"}, true)
	if err != nil {
		t.Fatalf("GuestExec: %v", err)
	}
	if pid != 42 {
		t.Errorf("PID: got %d, want 42", pid)
	}
}

func TestQGAGuestExecStatus(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("hello"))
	sockPath := filepath.Join(shortSockDir(t), "qga.sock")
	cleanup := mockSocketServer(t, sockPath, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		qgaReadLine(conn)
		_, _ = fmt.Fprintf(conn, `{"return":{"exited":true,"exitcode":0,"out-data":"%s"}}`+"\n", encoded)
	})
	defer cleanup()

	client, err := qemu.NewQGAClient(context.Background(), sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("NewQGAClient: %v", err)
	}
	defer func() { _ = client.Close() }()

	status, err := client.GuestExecStatus(42)
	if err != nil {
		t.Fatalf("GuestExecStatus: %v", err)
	}
	if !status.Exited {
		t.Error("expected Exited=true")
	}
	if status.ExitCode != 0 {
		t.Errorf("ExitCode: got %d, want 0", status.ExitCode)
	}
	if string(status.OutData) != "hello" {
		t.Errorf("OutData: got %q, want %q", string(status.OutData), "hello")
	}
}

func TestQGARunCommand(t *testing.T) {
	outEncoded := base64.StdEncoding.EncodeToString([]byte("output text\n"))
	sockPath := filepath.Join(shortSockDir(t), "qga.sock")
	cleanup := mockSocketServer(t, sockPath, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		// guest-exec
		qgaReadLine(conn)
		_, _ = fmt.Fprintf(conn, `{"return":{"pid":99}}`+"\n")
		// guest-exec-status
		qgaReadLine(conn)
		_, _ = fmt.Fprintf(conn, `{"return":{"exited":true,"exitcode":0,"out-data":"%s"}}`+"\n", outEncoded)
	})
	defer cleanup()

	client, err := qemu.NewQGAClient(context.Background(), sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("NewQGAClient: %v", err)
	}
	defer func() { _ = client.Close() }()

	stdout, _, exitCode, err := client.RunCommand("/bin/sh", []string{"-c", "echo output text"})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exitCode: got %d, want 0", exitCode)
	}
	if !strings.Contains(stdout, "output text") {
		t.Errorf("stdout: got %q, want to contain 'output text'", stdout)
	}
}

func TestQGAGuestNetworkGetInterfaces(t *testing.T) {
	payload := `{"return":[` +
		`{"name":"eth0","hardware-address":"52:54:00:ab:cd:01","ip-addresses":[{"ip-address":"192.168.1.10","ip-address-type":"ipv4","prefix":24}]},` +
		`{"name":"lo","hardware-address":"00:00:00:00:00:00","ip-addresses":[{"ip-address":"127.0.0.1","ip-address-type":"ipv4","prefix":8}]}` +
		`]}`

	sockPath := filepath.Join(shortSockDir(t), "qga.sock")
	cleanup := mockSocketServer(t, sockPath, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		qgaReadLine(conn)
		_, _ = fmt.Fprint(conn, payload+"\n")
	})
	defer cleanup()

	client, err := qemu.NewQGAClient(context.Background(), sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("NewQGAClient: %v", err)
	}
	defer func() { _ = client.Close() }()

	ifaces, err := client.GuestNetworkGetInterfaces()
	if err != nil {
		t.Fatalf("GuestNetworkGetInterfaces: %v", err)
	}
	if len(ifaces) != 2 {
		t.Fatalf("expected 2 interfaces, got %d", len(ifaces))
	}
	if ifaces[0].Name != "eth0" {
		t.Errorf("iface[0].Name: got %q, want eth0", ifaces[0].Name)
	}
	if len(ifaces[0].IPAddresses) == 0 || ifaces[0].IPAddresses[0].IPAddress != "192.168.1.10" {
		t.Errorf("iface[0] IP: got %v", ifaces[0].IPAddresses)
	}
}

func TestQGADialTimeout(t *testing.T) {
	_, err := qemu.NewQGAClient(context.Background(), "/nonexistent/qga-timeout.sock", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected dial error, got nil")
	}
}
