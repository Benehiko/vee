package vm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// fakeGuest listens on loopback and echoes one line per connection, standing
// in for the guest's sshd.
func fakeGuest(t *testing.T) (host string, port int) {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("guest listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				buf := make([]byte, 64)
				n, readErr := conn.Read(buf)
				if readErr != nil {
					return
				}
				_, _ = conn.Write(buf[:n])
			}()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

// TestSSHLoopbackProxyForwards covers the happy path: a client dialing the
// loopback port reaches the "guest", with the IP resolved per connection.
func TestSSHLoopbackProxyForwards(t *testing.T) {
	guestHost, guestPort := fakeGuest(t)

	// The resolver runs on the proxy's per-connection goroutines, so the
	// count must be atomic for -race.
	var resolves atomic.Int32
	resolve := func(context.Context) (string, error) {
		resolves.Add(1)
		return guestHost, nil
	}

	p, err := startSSHLoopbackProxy(context.Background(), "test", 0, guestPort, resolve, zap.NewNop())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer p.Close()

	for i := range 2 {
		var d net.Dialer
		conn, dialErr := d.DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Port())))
		if dialErr != nil {
			t.Fatalf("dial proxy: %v", dialErr)
		}
		msg := fmt.Sprintf("ping-%d", i)
		if _, err := conn.Write([]byte(msg)); err != nil {
			t.Fatalf("write: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 64)
		n, readErr := conn.Read(buf)
		if readErr != nil {
			t.Fatalf("read through proxy: %v", readErr)
		}
		if got := string(buf[:n]); got != msg {
			t.Errorf("echoed %q, want %q", got, msg)
		}
		_ = conn.Close()
	}

	// Per-connection resolution is what keeps the proxy working across DHCP
	// renewals; a cached resolve would break silently.
	if got := resolves.Load(); got != 2 {
		t.Errorf("resolver called %d times, want once per connection (2)", got)
	}
}

// TestSSHLoopbackProxyResolveFailure covers the guest-unreachable path (VM
// booting, no lease yet): the client connection must be closed promptly, not
// left hanging.
func TestSSHLoopbackProxyResolveFailure(t *testing.T) {
	resolve := func(context.Context) (string, error) {
		return "", errors.New("no lease yet")
	}
	p, err := startSSHLoopbackProxy(context.Background(), "test", 0, 22, resolve, zap.NewNop())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer p.Close()

	var d net.Dialer
	conn, dialErr := d.DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Port())))
	if dialErr != nil {
		t.Fatalf("dial proxy: %v", dialErr)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, readErr := conn.Read(make([]byte, 1)); readErr != io.EOF {
		t.Errorf("read = %v, want io.EOF (connection closed on resolve failure)", readErr)
	}
}

// TestSSHLoopbackProxyClose covers shutdown: after Close the port must be
// refused, so a stale state.SSHPort probe (cmd/ssh.go loopbackSSHAlive)
// correctly falls back to the LAN address.
func TestSSHLoopbackProxyClose(t *testing.T) {
	resolve := func(context.Context) (string, error) { return "127.0.0.1", nil }
	p, err := startSSHLoopbackProxy(context.Background(), "test", 0, 22, resolve, zap.NewNop())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	port := p.Port()
	p.Close()

	d := net.Dialer{Timeout: time.Second}
	if _, dialErr := d.DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); dialErr == nil {
		t.Error("dial succeeded after Close; listener still up")
	}
}
