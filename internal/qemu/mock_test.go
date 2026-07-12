package qemu_test

import (
	"context"
	"net"
	"testing"
)

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
