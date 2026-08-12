//go:build darwin

package shutdown

// macOS backend: maps launchd's shutdown contract onto the logind-shaped
// API the daemon consumes.
//
// launchd gives a daemon exactly one shutdown affordance: SIGTERM, then up
// to the job's ExitTimeOut seconds to exit, then SIGKILL. There is no
// pre-shutdown notification and no way to distinguish "launchctl bootout"
// from "the host is powering off" — so vee treats SIGTERM as host shutdown
// and gracefully stops running VMs either way (leaving them running would
// only mean they get hard-killed moments later as the host goes down).
//
// SIGINT (Ctrl-C on a manually run `vee daemon`) is deliberately NOT
// mapped: it cancels the daemon's context without marking a shutdown, so
// VMs are left running — matching `systemctl stop vee` on Linux.
//
// There is no shutdown-inhibitor analogue on macOS; the launchd plist's
// ExitTimeOut is what buys the graceful-stop window. Acquire instead
// prevents *idle sleep* while VMs run (via caffeinate), mirroring the
// sleep half of the Linux "shutdown:sleep" inhibitor.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// sigtermGrace bounds how long PreparingForShutdown waits for a SIGTERM to
// be recorded. The root command's own signal handler cancels the daemon's
// context on the same SIGTERM this package subscribes to, and the Go
// runtime delivers a signal to its subscribers one channel at a time — so
// the context can be observed cancelled a beat before the signal lands
// here. The wait costs 200ms on the genuine "plain stop" path (SIGINT),
// where the answer is a daemon about to exit anyway.
const sigtermGrace = 200 * time.Millisecond

// Conn subscribes to SIGTERM for the life of the daemon and records
// whether one has been seen, so the daemon can disambiguate its context
// being cancelled.
type Conn struct {
	mu     sync.Mutex
	subbed bool
	closed bool

	sigCh  chan os.Signal
	termCh chan struct{} // closed once SIGTERM is observed
	quit   chan struct{} // closed by Close to reap the watch goroutine
}

// Connect installs the SIGTERM watch. Close it when the daemon exits.
func Connect() (*Conn, error) {
	c := &Conn{
		sigCh:  make(chan os.Signal, 1),
		termCh: make(chan struct{}),
		quit:   make(chan struct{}),
	}
	signal.Notify(c.sigCh, syscall.SIGTERM)
	go func() {
		select {
		case <-c.sigCh:
			close(c.termCh)
		case <-c.quit:
		}
	}()
	return c, nil
}

// PrepareForShutdown returns a channel that fires once when SIGTERM
// arrives — launchd's only shutdown signal. The channel is never closed,
// so teardown cannot be mistaken for shutdown. May only be called once per
// Conn.
func (c *Conn) PrepareForShutdown(ctx context.Context) (<-chan struct{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("connection closed")
	}
	if c.subbed {
		return nil, errors.New("PrepareForShutdown already subscribed")
	}
	c.subbed = true

	out := make(chan struct{}, 1)
	go func() {
		select {
		case <-ctx.Done():
		case <-c.termCh:
			out <- struct{}{}
		}
	}()
	return out, nil
}

// PreparingForShutdown reports whether a SIGTERM has been received. It
// waits up to sigtermGrace for one to land, because the daemon's context
// is cancelled by the same signal and can be observed first.
func (c *Conn) PreparingForShutdown() (bool, error) {
	select {
	case <-c.termCh:
		return true, nil
	case <-time.After(sigtermGrace):
		return false, nil
	}
}

// Lock is a running `caffeinate -i` process asserting "no idle sleep"
// while at least one VM runs.
type Lock struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

// Acquire starts a caffeinate assertion preventing idle system sleep — the
// macOS analogue of the sleep half of the Linux shutdown:sleep inhibitor
// (blocking *shutdown* has no macOS API; the launchd job's ExitTimeOut
// covers that). `-w <pid>` bounds the assertion to the daemon's own
// lifetime even if Release is never called (e.g. the daemon crashes).
func (c *Conn) Acquire(_, _ string) (*Lock, error) {
	caff, err := exec.LookPath("caffeinate")
	if err != nil {
		return nil, fmt.Errorf("caffeinate not found: %w", err)
	}
	//nolint:gosec,noctx // caffeinate from LookPath with fixed args; lifetime is managed by Release / -w, not a context.
	cmd := exec.Command(caff, "-i", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start caffeinate: %w", err)
	}
	return &Lock{cmd: cmd}, nil
}

// Release stops the caffeinate process, dropping the sleep assertion. Safe
// to call multiple times.
func (l *Lock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cmd == nil {
		return nil
	}
	cmd := l.cmd
	l.cmd = nil
	err := cmd.Process.Kill()
	_ = cmd.Wait()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

// Close stops the SIGTERM watch. Any held Lock should be released first.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	signal.Stop(c.sigCh)
	close(c.quit)
	return nil
}
