// Package shutdown wraps the systemd-logind D-Bus API for blocking host
// shutdown until vee can gracefully power off running VMs.
//
// It provides:
//   - Acquire: takes an Inhibit lock in "block" mode for shutdown:sleep,
//     so logind will not let the host power off until the lock is released.
//   - PrepareForShutdown: subscribes to logind's PrepareForShutdown(b)
//     signal so the daemon learns when the host actually starts shutting
//     down and can begin its graceful-stop sequence.
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

// Inhibitor holds an active logind inhibitor lock and an optional system
// bus connection used for PrepareForShutdown signal subscription.
type Inhibitor struct {
	mu     sync.Mutex
	fd     *os.File
	conn   *dbus.Conn
	signal chan *dbus.Signal
}

// Acquire takes a "block" inhibitor for shutdown:sleep on the system bus.
// `who` and `why` are surfaced by `systemd-inhibit --list`. The returned
// Inhibitor must be released with Release when the daemon exits.
func Acquire(who, why string) (*Inhibitor, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connect system bus: %w", err)
	}

	obj := conn.Object(logindDest, logindPath)
	var fd dbus.UnixFD
	call := obj.Call(inhibitMethod, 0, defaultWhat, who, why, defaultMode)
	if call.Err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("inhibit call: %w", call.Err)
	}
	if err := call.Store(&fd); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("inhibit fd: %w", err)
	}

	return &Inhibitor{
		fd:   os.NewFile(uintptr(fd), "logind-inhibit"),
		conn: conn,
	}, nil
}

// PrepareForShutdown subscribes to logind's PrepareForShutdown(b) signal
// and returns a channel that receives exactly once when shutdown begins
// (signal payload true). Unsubscribes when ctx is cancelled.
func (i *Inhibitor) PrepareForShutdown(ctx context.Context) (<-chan struct{}, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.conn == nil {
		return nil, errors.New("inhibitor not active")
	}
	if i.signal != nil {
		return nil, errors.New("PrepareForShutdown already subscribed")
	}

	rule := fmt.Sprintf(
		"type='signal',interface='%s',member='%s',path='%s'",
		logindManager, prepareSignal, logindPath,
	)
	if call := i.conn.BusObject().Call(
		"org.freedesktop.DBus.AddMatch", 0, rule,
	); call.Err != nil {
		return nil, fmt.Errorf("AddMatch PrepareForShutdown: %w", call.Err)
	}

	sigCh := make(chan *dbus.Signal, 4)
	i.conn.Signal(sigCh)
	i.signal = sigCh

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
// PreparingForShutdown property. True while a shutdown is in progress (after
// PrepareForShutdown(true) has fired and before PrepareForShutdown(false)).
// Use this on ctx.Done() to disambiguate "user stopped the daemon" from
// "daemon received SIGTERM during host shutdown".
func (i *Inhibitor) PreparingForShutdown() (bool, error) {
	i.mu.Lock()
	conn := i.conn
	i.mu.Unlock()
	if conn == nil {
		return false, errors.New("inhibitor not active")
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

// Release closes the held file descriptor (releasing the inhibitor) and
// the system bus connection. Safe to call multiple times.
func (i *Inhibitor) Release() error {
	i.mu.Lock()
	defer i.mu.Unlock()

	var firstErr error
	if i.fd != nil {
		if err := i.fd.Close(); err != nil {
			firstErr = err
		}
		i.fd = nil
	}
	if i.conn != nil {
		if err := i.conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		i.conn = nil
	}
	return firstErr
}
