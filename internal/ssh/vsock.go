//go:build linux

package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const vsockPort = 2222

// Proxy listens on AF_VSOCK port 2222 and forwards each connection to the
// host SSH_AUTH_SOCK. This allows guests to use the host SSH agent without
// copying keys into the VM.
type Proxy struct {
	agentSock string
	listener  net.Listener
}

// NewProxy creates a vsock proxy that will forward to agentSock.
// If agentSock is empty, SSH_AUTH_SOCK from the environment is used.
func NewProxy(agentSock string) (*Proxy, error) {
	if agentSock == "" {
		agentSock = os.Getenv("SSH_AUTH_SOCK")
	}
	if agentSock == "" {
		return nil, fmt.Errorf("SSH_AUTH_SOCK not set and no socket path provided")
	}
	// agentSock is an operator-supplied SSH agent socket path (flag or
	// SSH_AUTH_SOCK); statting it to validate its existence is the intended
	// behavior, not attacker-controlled path traversal.
	if _, err := os.Stat(agentSock); err != nil { //nolint:gosec // trusted operator-supplied SSH agent socket path
		return nil, fmt.Errorf("SSH agent socket %s: %w", agentSock, err)
	}
	return &Proxy{agentSock: agentSock}, nil
}

// Listen binds the AF_VSOCK socket. Must be called before Serve.
func (p *Proxy) Listen() error {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("create vsock: %w", err)
	}

	sa := &unix.SockaddrVM{
		CID:  unix.VMADDR_CID_ANY,
		Port: vsockPort,
	}
	if err := unix.Bind(fd, sa); err != nil {
		_ = unix.Close(fd)
		return fmt.Errorf("bind vsock port %d: %w", vsockPort, err)
	}
	if err := unix.Listen(fd, 8); err != nil {
		_ = unix.Close(fd)
		return fmt.Errorf("listen vsock: %w", err)
	}

	p.listener = &vsockListener{fd: fd}
	return nil
}

// Serve accepts connections and proxies them to SSH_AUTH_SOCK.
// Blocks until the listener is closed.
func (p *Proxy) Serve() error {
	return p.ServeContext(context.Background())
}

// ServeContext accepts connections and proxies them to SSH_AUTH_SOCK, using ctx
// for the dial to the host agent socket. Blocks until the listener is closed.
func (p *Proxy) ServeContext(ctx context.Context) error {
	if p.listener == nil {
		return fmt.Errorf("call Listen before Serve")
	}
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return err
		}
		go p.handle(ctx, conn)
	}
}

// Close shuts down the vsock listener.
func (p *Proxy) Close() error {
	if p.listener != nil {
		return p.listener.Close()
	}
	return nil
}

func (p *Proxy) handle(ctx context.Context, guest net.Conn) {
	defer func() { _ = guest.Close() }()

	var d net.Dialer
	agent, err := d.DialContext(ctx, "unix", p.agentSock)
	if err != nil {
		return
	}
	defer func() { _ = agent.Close() }()

	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(agent, guest)
	go cp(guest, agent)
	<-done
}

// vsockListener wraps a raw AF_VSOCK file descriptor into net.Listener.
type vsockListener struct {
	fd int
}

func (l *vsockListener) Accept() (net.Conn, error) {
	nfd, _, err := unix.Accept(l.fd)
	if err != nil {
		return nil, err
	}
	return &vsockConn{fd: nfd}, nil
}

func (l *vsockListener) Close() error {
	return unix.Close(l.fd)
}

func (l *vsockListener) Addr() net.Addr {
	return &vsockAddr{port: vsockPort}
}

type vsockConn struct {
	fd int
}

func (c *vsockConn) Read(b []byte) (int, error)         { return unix.Read(c.fd, b) }
func (c *vsockConn) Write(b []byte) (int, error)        { return unix.Write(c.fd, b) }
func (c *vsockConn) Close() error                       { return unix.Close(c.fd) }
func (c *vsockConn) LocalAddr() net.Addr                { return &vsockAddr{port: vsockPort} }
func (c *vsockConn) RemoteAddr() net.Addr               { return &vsockAddr{} }
func (c *vsockConn) SetDeadline(_ time.Time) error      { return nil }
func (c *vsockConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *vsockConn) SetWriteDeadline(_ time.Time) error { return nil }

type vsockAddr struct {
	port uint32
}

func (a *vsockAddr) Network() string { return "vsock" }
func (a *vsockAddr) String() string  { return fmt.Sprintf("vsock:%d", a.port) }
