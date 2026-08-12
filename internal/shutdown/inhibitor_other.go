//go:build !linux && !darwin

package shutdown

// Stub backend for platforms with no host-shutdown integration. Connect
// fails, the daemon logs a warning, and runs without shutdown handling —
// the same net behaviour the Linux backend has when the system bus is
// unreachable.

import (
	"context"
	"errors"
	"runtime"
)

var errUnsupported = errors.New("host shutdown integration is not supported on " + runtime.GOOS)

// Conn is never instantiated on this platform; it exists so callers
// compile unchanged.
type Conn struct{}

// Connect always fails on this platform.
func Connect() (*Conn, error) { return nil, errUnsupported }

// PrepareForShutdown is unreachable (Connect never succeeds).
func (c *Conn) PrepareForShutdown(context.Context) (<-chan struct{}, error) {
	return nil, errUnsupported
}

// PreparingForShutdown is unreachable (Connect never succeeds).
func (c *Conn) PreparingForShutdown() (bool, error) { return false, errUnsupported }

// Acquire is unreachable (Connect never succeeds).
func (c *Conn) Acquire(_, _ string) (*Lock, error) { return nil, errUnsupported }

// Close is a no-op.
func (c *Conn) Close() error { return nil }

// Lock is never instantiated on this platform.
type Lock struct{}

// Release is a no-op.
func (l *Lock) Release() error { return nil }
