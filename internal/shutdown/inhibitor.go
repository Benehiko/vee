// Package shutdown wraps the systemd-logind D-Bus API for blocking host
// shutdown until vee can gracefully power off running VMs.
//
// Two-layer design:
//
//   - Conn: a long-lived system-bus connection owned by the daemon for its
//     entire lifetime. It exposes PrepareForShutdown subscription (which
//     must outlive any individual lock) and Acquire/Release for the
//     transient inhibitor fd.
//   - Lock: the inhibitor file descriptor itself. Releasing the lock is
//     what unblocks logind. The daemon takes a Lock only while ≥1 VM is
//     running so the host is not blocked when there is nothing to wait for.
package shutdown

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	dbus "github.com/godbus/dbus/v5"
)

const (
	logindDest    = "org.freedesktop.login1"
	logindPath    = dbus.ObjectPath("/org/freedesktop/login1")
	logindManager = "org.freedesktop.login1.Manager"
	inhibitMethod = logindManager + ".Inhibit"
	prepareSignal = "PrepareForShutdown"
	defaultWhat   = "shutdown:sleep"
	defaultMode   = "block"
)

// Conn is a persistent system-bus connection used to subscribe to
// PrepareForShutdown and to acquire/release inhibitor locks on demand.
type Conn struct {
	mu     sync.Mutex
	conn   *dbus.Conn
	signal chan *dbus.Signal
	subbed bool
}

// Connect opens a system-bus connection. Close it with Conn.Close when the
// daemon exits.
func Connect() (*Conn, error) {
	c, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connect system bus: %w", err)
	}
	return &Conn{conn: c}, nil
}

// Lock is an active "block" inhibitor on shutdown:sleep. Releasing it
// closes the file descriptor logind handed us, which is what actually
// unblocks shutdown.
type Lock struct {
	mu sync.Mutex
	fd *os.File
}

// Acquire takes a "block" inhibitor for shutdown:sleep. `who` and `why`
// are surfaced by `systemd-inhibit --list`. The returned Lock must be
// released when no longer needed.
func (c *Conn) Acquire(who, why string) (*Lock, error) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return nil, errors.New("connection closed")
	}

	obj := conn.Object(logindDest, logindPath)
	var fd dbus.UnixFD
	call := obj.Call(inhibitMethod, 0, defaultWhat, who, why, defaultMode)
	if call.Err != nil {
		return nil, fmt.Errorf("inhibit call: %w", call.Err)
	}
	if err := call.Store(&fd); err != nil {
		return nil, fmt.Errorf("inhibit fd: %w", err)
	}
	return &Lock{fd: os.NewFile(uintptr(fd), "logind-inhibit")}, nil
}

// Release closes the held file descriptor, releasing the inhibitor. Safe
// to call multiple times.
func (l *Lock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fd == nil {
		return nil
	}
	err := l.fd.Close()
	l.fd = nil
	return err
}

// PrepareForShutdown subscribes to logind's PrepareForShutdown(b) signal
// and returns a channel that receives exactly once when shutdown begins
// (signal payload true). Unsubscribes when ctx is cancelled. May only be
// called once per Conn.
func (c *Conn) PrepareForShutdown(ctx context.Context) (<-chan struct{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil, errors.New("connection closed")
	}
	if c.subbed {
		return nil, errors.New("PrepareForShutdown already subscribed")
	}

	rule := fmt.Sprintf(
		"type='signal',interface='%s',member='%s',path='%s'",
		logindManager, prepareSignal, logindPath,
	)
	if call := c.conn.BusObject().Call(
		"org.freedesktop.DBus.AddMatch", 0, rule,
	); call.Err != nil {
		return nil, fmt.Errorf("AddMatch PrepareForShutdown: %w", call.Err)
	}

	sigCh := make(chan *dbus.Signal, 4)
	c.conn.Signal(sigCh)
	c.signal = sigCh
	c.subbed = true

	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case sig, ok := <-sigCh:
				if !ok {
					return
				}
				if sig.Path != logindPath ||
					sig.Name != logindManager+"."+prepareSignal {
					continue
				}
				if len(sig.Body) < 1 {
					continue
				}
				active, _ := sig.Body[0].(bool)
				if active {
					out <- struct{}{}
					return
				}
			}
		}
	}()
	return out, nil
}

// PreparingForShutdown returns the current value of logind's
// PreparingForShutdown property. True while a shutdown is in progress
// (after PrepareForShutdown(true) has fired and before
// PrepareForShutdown(false)). Use this on ctx.Done() to disambiguate
// "user stopped the daemon" from "daemon received SIGTERM during host
// shutdown".
func (c *Conn) PreparingForShutdown() (bool, error) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return false, errors.New("connection closed")
	}
	obj := conn.Object(logindDest, logindPath)
	v, err := obj.GetProperty(logindManager + ".PreparingForShutdown")
	if err != nil {
		return false, fmt.Errorf("get PreparingForShutdown: %w", err)
	}
	b, ok := v.Value().(bool)
	if !ok {
		return false, fmt.Errorf("PreparingForShutdown not a bool: %T", v.Value())
	}
	return b, nil
}

// Close tears down the system-bus connection. Any held Lock should be
// released first.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}
