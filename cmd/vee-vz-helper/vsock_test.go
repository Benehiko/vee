package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Benehiko/vee/internal/vzhelper"
)

// shortTempDir returns a freshly created directory with a short path: unix
// socket paths are capped (104 bytes on macOS), and t.TempDir() embeds the
// full test name.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "vsk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakeDevice implements guestVsock without Virtualization.framework.
type fakeDevice struct {
	connect func(port uint32) (net.Conn, error)
	listen  func(port uint32) (net.Listener, error)
}

func (f *fakeDevice) Connect(port uint32) (net.Conn, error)    { return f.connect(port) }
func (f *fakeDevice) Listen(port uint32) (net.Listener, error) { return f.listen(port) }

// chanListener lets a test inject "guest-initiated" connections.
type chanListener struct {
	ch        chan net.Conn
	closeOnce sync.Once
	done      chan struct{}
}

func newChanListener() *chanListener {
	return &chanListener{ch: make(chan net.Conn), done: make(chan struct{})}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, errors.New("listener closed")
	}
}

func (l *chanListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return nil
}

func (l *chanListener) Addr() net.Addr { return &net.UnixAddr{Name: "fake-vsock", Net: "unix"} }

func TestVsockOpsDisabledWithoutDevice(t *testing.T) {
	s := newVsockState(shortTempDir(t), nil)
	if _, err := s.connectBridge(1); !errors.Is(err, errVsockDisabled) {
		t.Errorf("connectBridge error = %v, want errVsockDisabled", err)
	}
	if err := s.listenForward(1, "/run/x.sock"); !errors.Is(err, errVsockDisabled) {
		t.Errorf("listenForward error = %v, want errVsockDisabled", err)
	}
}

func TestVsockOpValidation(t *testing.T) {
	dev := &fakeDevice{
		connect: func(uint32) (net.Conn, error) { t.Fatal("connect must not be reached"); return nil, nil },
		listen:  func(uint32) (net.Listener, error) { t.Fatal("listen must not be reached"); return nil, nil },
	}
	s := newVsockState(shortTempDir(t), dev)
	if _, err := s.connectBridge(0); err == nil || !strings.Contains(err.Error(), "port is required") {
		t.Errorf("connectBridge(0) error = %v, want port-required", err)
	}
	if err := s.listenForward(0, "/run/x.sock"); err == nil || !strings.Contains(err.Error(), "port is required") {
		t.Errorf("listenForward(0) error = %v, want port-required", err)
	}
	if err := s.listenForward(5, "relative.sock"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("listenForward(relative) error = %v, want absolute-path", err)
	}
}

// TestConnectBridge covers the OpVsockConnect path: the bridge socket is
// published under the VM dir, is idempotent per port, and splices a host
// connection with a fresh guest connection.
func TestConnectBridge(t *testing.T) {
	dir := shortTempDir(t)

	// The fake guest echoes one line per connection, prefixed so the test can
	// prove the data crossed the "vsock" hop.
	guestEcho := func(port uint32) (net.Conn, error) {
		host, guest := net.Pipe()
		go func() {
			defer func() { _ = guest.Close() }()
			line, err := bufio.NewReader(guest).ReadString('\n')
			if err != nil {
				return
			}
			_, _ = fmt.Fprintf(guest, "port %d: %s", port, line)
		}()
		return host, nil
	}
	s := newVsockState(dir, &fakeDevice{connect: guestEcho})
	defer s.closeAll()

	path, err := s.connectBridge(7)
	if err != nil {
		t.Fatalf("connectBridge: %v", err)
	}
	if want := vzhelper.VsockBridgePath(dir, 7); path != want {
		t.Errorf("bridge path = %q, want %q", path, want)
	}
	again, err := s.connectBridge(7)
	if err != nil || again != path {
		t.Errorf("repeat connectBridge = %q, %v; want same path, nil", again, err)
	}

	conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", path)
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if reply != "port 7: ping\n" {
		t.Errorf("reply = %q, want %q", reply, "port 7: ping\n")
	}
}

// TestListenForward covers the OpVsockListen path: guest-initiated
// connections are spliced with the host unix socket, and a repeated op
// retargets the forward instead of double-listening.
func TestListenForward(t *testing.T) {
	dir := shortTempDir(t)

	ln := newChanListener()
	listenCalls := 0
	s := newVsockState(dir, &fakeDevice{listen: func(uint32) (net.Listener, error) {
		listenCalls++
		return ln, nil
	}})
	defer s.closeAll()

	// Host-side echo server the forward should deliver guest connections to.
	hostSock := dir + "/host.sock"
	hostLn, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", hostSock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hostLn.Close() }()
	go func() {
		for {
			c, err := hostLn.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				line, err := bufio.NewReader(c).ReadString('\n')
				if err != nil {
					return
				}
				_, _ = fmt.Fprintf(c, "host: %s", line)
			}()
		}
	}()

	if err := s.listenForward(9, hostSock); err != nil {
		t.Fatalf("listenForward: %v", err)
	}
	if err := s.listenForward(9, hostSock); err != nil {
		t.Fatalf("repeat listenForward: %v", err)
	}
	if listenCalls != 1 {
		t.Errorf("device Listen called %d times, want 1 (repeat must retarget, not re-listen)", listenCalls)
	}

	// Inject a "guest" connection and speak through the forward.
	guest, injected := net.Pipe()
	select {
	case ln.ch <- injected:
	case <-time.After(5 * time.Second):
		t.Fatal("forward never accepted the guest connection")
	}
	_ = guest.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := guest.Write([]byte("hello\n")); err != nil {
		t.Fatalf("guest write: %v", err)
	}
	reply, err := bufio.NewReader(guest).ReadString('\n')
	if err != nil {
		t.Fatalf("guest read: %v", err)
	}
	if reply != "host: hello\n" {
		t.Errorf("reply = %q, want %q", reply, "host: hello\n")
	}
}

// TestCloseAllRemovesBridgeSockets makes sure a stopping VM leaves no bridge
// sockets behind for the next run to mistake for live ones.
func TestCloseAllRemovesBridgeSockets(t *testing.T) {
	dir := shortTempDir(t)
	s := newVsockState(dir, &fakeDevice{connect: func(uint32) (net.Conn, error) {
		return nil, errors.New("unused")
	}})
	path, err := s.connectBridge(3)
	if err != nil {
		t.Fatalf("connectBridge: %v", err)
	}
	s.closeAll()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("bridge socket still present after closeAll: stat err = %v", err)
	}
	if _, err := (&net.Dialer{}).DialContext(context.Background(), "unix", path); err == nil {
		t.Error("bridge socket still accepting after closeAll")
	}
}
