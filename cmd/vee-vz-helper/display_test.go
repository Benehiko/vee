package main

import (
	"errors"
	"testing"
)

// The display gate is the whole once-per-run policy for the native window
// (issue #139): first request accepted and signalled, concurrent request
// refused as "open", any request after the window closed refused with
// restart advice — never a second signal, because AppKit cannot re-run the
// window's event loop.
func TestDisplayGate(t *testing.T) {
	g := newDisplayGate()

	if err := g.request(); err != nil {
		t.Fatalf("first request: %v", err)
	}
	select {
	case <-g.requests():
	default:
		t.Fatal("accepted request did not signal the main goroutine")
	}

	if err := g.request(); !errors.Is(err, errDisplayOpen) {
		t.Errorf("request while open = %v, want errDisplayOpen", err)
	}

	g.windowClosed()
	if err := g.request(); !errors.Is(err, errDisplayClosed) {
		t.Errorf("request after close = %v, want errDisplayClosed", err)
	}

	select {
	case <-g.requests():
		t.Error("refused requests must not signal the main goroutine")
	default:
	}
}

// A window can close because the VM stopped under it before anyone asked to
// see it never happens (windowClosed without request) — but a defensive
// close from the idle state must still pin the gate shut rather than corrupt
// it.
func TestDisplayGateCloseWithoutRequest(t *testing.T) {
	g := newDisplayGate()
	g.windowClosed()
	if err := g.request(); !errors.Is(err, errDisplayClosed) {
		t.Errorf("request after unpaired close = %v, want errDisplayClosed", err)
	}
}
