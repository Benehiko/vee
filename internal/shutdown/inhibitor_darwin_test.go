//go:build darwin

package shutdown

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestSIGTERMReportsShutdown verifies the launchd mapping: SIGTERM fires
// the PrepareForShutdown channel and flips PreparingForShutdown, so the
// daemon takes the graceful-stop path. Sending SIGTERM to the test process
// is safe here because Connect has already subscribed a handler for it.
func TestSIGTERMReportsShutdown(t *testing.T) {
	c, err := Connect()
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	ch, err := c.PrepareForShutdown(context.Background())
	if err != nil {
		t.Fatalf("PrepareForShutdown: %v", err)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("kill: %v", err)
	}

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("PrepareForShutdown channel did not fire on SIGTERM")
	}

	preparing, err := c.PreparingForShutdown()
	if err != nil {
		t.Fatalf("PreparingForShutdown: %v", err)
	}
	if !preparing {
		t.Fatal("PreparingForShutdown = false after SIGTERM")
	}
}

// TestPlainStopIsNotShutdown verifies that without a SIGTERM (e.g. Ctrl-C
// cancelling the daemon's context) nothing reports a shutdown, so VMs are
// left running.
func TestPlainStopIsNotShutdown(t *testing.T) {
	c, err := Connect()
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.PrepareForShutdown(ctx)
	if err != nil {
		t.Fatalf("PrepareForShutdown: %v", err)
	}
	cancel()

	preparing, err := c.PreparingForShutdown()
	if err != nil {
		t.Fatalf("PreparingForShutdown: %v", err)
	}
	if preparing {
		t.Fatal("PreparingForShutdown = true without SIGTERM")
	}
	select {
	case <-ch:
		t.Fatal("PrepareForShutdown channel fired without SIGTERM")
	default:
	}
}

func TestPrepareForShutdownOncePerConn(t *testing.T) {
	c, err := Connect()
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	if _, err := c.PrepareForShutdown(context.Background()); err != nil {
		t.Fatalf("first PrepareForShutdown: %v", err)
	}
	if _, err := c.PrepareForShutdown(context.Background()); err == nil {
		t.Fatal("second PrepareForShutdown did not error")
	}
}

func TestAcquireReleaseCaffeinate(t *testing.T) {
	if _, err := exec.LookPath("caffeinate"); err != nil {
		t.Skip("caffeinate not available")
	}
	c, err := Connect()
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	l, err := c.Acquire("vee", "test assertion")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Release is documented as safe to call multiple times.
	if err := l.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}
