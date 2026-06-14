package qemu_test

import (
	"context"
	"net"
	"os"
	"testing"
)

// shortSockDir returns a short-lived temporary directory suitable for AF_UNIX
// socket paths. On macOS the per-test t.TempDir() path under /var/folders can
// exceed the 104-byte sun_path limit (bind: invalid argument), so prefer a
// short /tmp-based directory and fall back to t.TempDir() if /tmp is unavailable.
func shortSockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "vee")
	if err != nil {
		return t.TempDir()
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// mockSocketServer starts a Unix socket server at sockPath that accepts one
// connection and runs handler in a goroutine. Returns a cleanup function.
func mockSocketServer(t *testing.T, sockPath string, handler func(net.Conn)) func() {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sockPath, err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		handler(conn)
	}()
	return func() {
		_ = ln.Close()
		<-done
	}
}
