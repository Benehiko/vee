package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/Benehiko/vee/internal/vzhelper"
)

// guestVsock abstracts the Virtualization.framework virtio socket device
// (VZVirtioSocketDevice) so the bridging logic below stays free of cgo and
// testable on every platform.
type guestVsock interface {
	// Connect opens a host-initiated connection to a guest-listening vsock
	// port.
	Connect(port uint32) (net.Conn, error)
	// Listen accepts guest-initiated vsock connections to a port.
	Listen(port uint32) (net.Listener, error)
}

// errVsockDisabled answers vsock ops on a VM whose machine spec did not
// enable the device. vee translates it into vm.yaml advice.
var errVsockDisabled = errors.New("vsock is not enabled in the machine spec")

// vsockState owns the unix-socket bridges and forwards created by the control
// protocol's vsock ops. Safe for concurrent use — one control connection per
// caller.
type vsockState struct {
	vmDir string
	dev   guestVsock // nil when the machine spec did not enable vsock

	mu       sync.Mutex
	bridges  map[uint32]net.Listener  // guest port → host unix bridge listener
	forwards map[uint32]*vsockForward // host-facing vsock port → forward
}

func newVsockState(vmDir string, dev guestVsock) *vsockState {
	return &vsockState{
		vmDir:    vmDir,
		dev:      dev,
		bridges:  make(map[uint32]net.Listener),
		forwards: make(map[uint32]*vsockForward),
	}
}

// connectBridge serves OpVsockConnect: it publishes a unix socket inside the
// VM directory and opens a fresh host→guest vsock connection to port for
// every connection accepted there. Idempotent — repeating the op returns the
// same path.
func (s *vsockState) connectBridge(port uint32) (string, error) {
	if s.dev == nil {
		return "", errVsockDisabled
	}
	if port == 0 {
		return "", fmt.Errorf("%s: port is required", vzhelper.OpVsockConnect)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := vzhelper.VsockBridgePath(s.vmDir, port)
	if _, ok := s.bridges[port]; ok {
		return path, nil
	}
	// A previous helper for this VM may have left its socket behind.
	_ = os.Remove(path)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", path)
	if err != nil {
		return "", fmt.Errorf("listen on vsock bridge %s: %w", path, err)
	}
	s.bridges[port] = ln
	go s.serveBridge(ln, port)
	return path, nil
}

func (s *vsockState) serveBridge(ln net.Listener, port uint32) {
	for {
		host, err := ln.Accept()
		if err != nil {
			return // listener closed on shutdown
		}
		go func() {
			guest, err := s.dev.Connect(port)
			if err != nil {
				fmt.Printf("vee-vz-helper: vsock bridge: connect guest port %d: %v\n", port, err)
				_ = host.Close()
				return
			}
			splice(host, guest)
		}()
	}
}

// listenForward serves OpVsockListen: guest-initiated vsock connections to
// port are each forwarded to the host unix socket at target. Repeating the op
// for a port retargets its forward rather than double-listening (the
// framework holds one listener per port).
func (s *vsockState) listenForward(port uint32, target string) error {
	if s.dev == nil {
		return errVsockDisabled
	}
	if port == 0 {
		return fmt.Errorf("%s: port is required", vzhelper.OpVsockListen)
	}
	if !filepath.IsAbs(target) {
		return fmt.Errorf("%s: path must be an absolute unix socket path (got %q)", vzhelper.OpVsockListen, target)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.forwards[port]; ok {
		f.setTarget(target)
		return nil
	}
	ln, err := s.dev.Listen(port)
	if err != nil {
		return fmt.Errorf("listen on guest vsock port %d: %w", port, err)
	}
	f := &vsockForward{ln: ln, target: target}
	s.forwards[port] = f
	go f.serve(port)
	return nil
}

// closeAll tears down every bridge and forward; called when the VM stops.
func (s *vsockState) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for port, ln := range s.bridges {
		_ = ln.Close()
		_ = os.Remove(vzhelper.VsockBridgePath(s.vmDir, port))
	}
	s.bridges = make(map[uint32]net.Listener)
	for _, f := range s.forwards {
		_ = f.ln.Close()
	}
	s.forwards = make(map[uint32]*vsockForward)
}

// vsockForward relays guest-initiated vsock connections to a host unix
// socket. The target is mutable so a repeated OpVsockListen can retarget the
// forward without touching the framework listener.
type vsockForward struct {
	ln net.Listener

	mu     sync.Mutex
	target string
}

func (f *vsockForward) setTarget(target string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.target = target
}

func (f *vsockForward) getTarget() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.target
}

func (f *vsockForward) serve(port uint32) {
	for {
		guest, err := f.ln.Accept()
		if err != nil {
			return // listener closed on shutdown
		}
		go func() {
			target := f.getTarget()
			host, err := (&net.Dialer{}).DialContext(context.Background(), "unix", target)
			if err != nil {
				fmt.Printf("vee-vz-helper: vsock forward: guest port %d → %s: %v\n", port, target, err)
				_ = guest.Close()
				return
			}
			splice(guest, host)
		}()
	}
}

// splice copies bytes both ways, closing both conns once either direction
// ends so the peer's blocked copy unwinds.
func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}
