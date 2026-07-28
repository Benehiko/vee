//go:build !windows

package qemu

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestDetachedSpawnSurvivesContextCancel pins down the process-lifetime
// contract a detached VM depends on: the child must outlive the command that
// started it.
//
// This is a regression test for a real failure. exec.CommandContext kills the
// child when its context is cancelled, and vee's root command cancels its
// signal context as soon as the command returns — so spawning a VM with the
// request context SIGKILLed it the instant `vee start` exited. It only bites
// when the parent also keeps a cmd.Wait goroutine instead of calling
// Process.Release (Release makes the kill a no-op), which is exactly the shape
// both backends use to reap their child and keep liveness checks honest.
func TestDetachedSpawnSurvivesContextCancel(t *testing.T) {
	cases := []struct {
		name        string
		withoutCanc bool
		wantAlive   bool
	}{
		// What the code does now: detached spawns are immune to cancellation.
		{name: "WithoutCancel", withoutCanc: true, wantAlive: true},
		// What the code did before, kept as the counter-example so the test
		// fails loudly if anyone reverts to passing the request context.
		{name: "PlainContext", withoutCanc: false, wantAlive: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())

			spawnCtx := ctx
			if tc.withoutCanc {
				spawnCtx = context.WithoutCancel(ctx)
			}
			cmd := exec.CommandContext(spawnCtx, "/bin/sh", "-c", "sleep 30")
			setDetachAttrs(cmd)
			if err := cmd.Start(); err != nil {
				t.Fatalf("start: %v", err)
			}
			pid := cmd.Process.Pid
			// The reaper both backends run; note there is deliberately no
			// Process.Release, which is what leaves the kill path armed.
			go func() { _ = cmd.Wait() }()
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

			cancel()
			// Give the kill path time to run if it is going to.
			deadline := time.Now().Add(2 * time.Second)
			alive := true
			for time.Now().Before(deadline) {
				if err := syscall.Kill(pid, 0); err != nil {
					alive = false
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if alive != tc.wantAlive {
				t.Errorf("child alive after cancel = %v, want %v", alive, tc.wantAlive)
			}
		})
	}
}
