package main

import (
	"errors"
	"sync"
)

// displayGate serializes native-display requests (issue #139) between control
// connections and the main goroutine, which is the only place the window can
// be presented: the vz bindings run NSApplication's event loop for the
// window's lifetime, and AppKit only functions on the process's main thread.
// The helper can present at most one window per VM run — AppKit cannot
// restart that event loop once it terminates — so the gate accepts the first
// request, reports "already open" while the window is up, and "restart the
// VM" after it closed. Kept free of cgo so it stays testable on every
// platform, like the vsock bridging.
type displayGate struct {
	mu    sync.Mutex
	state int
	// ch signals the main goroutine to present the window. Buffered so the
	// control handler never blocks on it; the state machine guarantees at
	// most one signal is ever in flight.
	ch chan struct{}
}

const (
	displayIdle = iota
	displayOpen
	displayClosed
)

var (
	errDisplayOpen   = errors.New("the display window is already open")
	errDisplayClosed = errors.New("the display window was already opened and closed — the helper can present it once per VM run; stop and start the VM to open a new one")
)

func newDisplayGate() *displayGate {
	return &displayGate{ch: make(chan struct{}, 1)}
}

// request claims the display. On nil the main goroutine has been signalled
// (via requests) to present the window.
func (g *displayGate) request() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.state {
	case displayOpen:
		return errDisplayOpen
	case displayClosed:
		return errDisplayClosed
	}
	g.state = displayOpen
	g.ch <- struct{}{}
	return nil
}

// requests delivers one signal per accepted request.
func (g *displayGate) requests() <-chan struct{} { return g.ch }

// windowClosed records that the presented window is gone — the user closed
// it, or the VM stopped underneath it.
func (g *displayGate) windowClosed() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state = displayClosed
}
