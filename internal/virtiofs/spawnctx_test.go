//go:build !windows

package virtiofs

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Benehiko/vee/provider"
)

// fakeProvider points virtiofsd at a stand-in binary.
type fakeProvider struct{ cfg *provider.Config }

func (p fakeProvider) Config() *provider.Config { return p.cfg }
func (p fakeProvider) Logger() *zap.Logger      { return zap.NewNop() }
func (p fakeProvider) DB() *sql.DB              { return nil }

// stubVirtiofsd writes a script that behaves like a long-running virtiofsd:
// it ignores its arguments and stays alive until it is killed.
func stubVirtiofsd(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "virtiofsd")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 30\n"), 0o700); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	return path
}

// TestStartDetachedSurvivesContextCancel pins down the process-lifetime contract
// a virtiofs share depends on: virtiofsd must outlive the command that started
// it.
//
// This is a regression test for a real failure. exec.CommandContext kills the
// child when its context is cancelled, and vee's root command cancels its signal
// context as soon as the command returns — so a virtiofsd spawned with the
// request context was SIGKILLed the instant `vee start` exited. QEMU wires the
// share as vhost-user-fs-pci over a chardev socket with no reconnect, so the
// guest's mount then failed for the rest of the VM's life, and the kill landed
// after vee had already reported the VM ready.
//
// The equivalent contract for the VM processes themselves is covered by
// internal/qemu's TestDetachedSpawnSurvivesContextCancel. This one exists
// because that fix was applied to the VM spawns and missed the helper beside
// them.
func TestStartDetachedSurvivesContextCancel(t *testing.T) {
	vd := NewVirtiofsd(
		fakeProvider{cfg: &provider.Config{VirtiofsdPath: stubVirtiofsd(t)}},
		WithVirtiofsdSharedDir(t.TempDir()),
		WithVirtiofsdTag("test"),
	)

	ctx, cancel := context.WithCancel(context.Background())
	pid, err := vd.StartDetached(ctx)
	if err != nil {
		t.Fatalf("StartDetached: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	cancel()

	// Give the kill path time to run if it is going to. A killed child with no
	// reaper becomes a zombie, which still answers signal 0 — so waitpid is the
	// only honest liveness check here.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processLive(t, pid) {
			t.Fatal("virtiofsd was killed when the caller's context was cancelled; " +
				"a detached share must outlive the command that started it")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// processLive reports whether pid is still running. It reaps first, so a child
// that was killed and left unreaped is reported dead rather than alive.
func processLive(t *testing.T, pid int) bool {
	t.Helper()
	var status syscall.WaitStatus
	reaped, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	if err == nil && reaped == pid {
		// Exited, and we have just collected it.
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
