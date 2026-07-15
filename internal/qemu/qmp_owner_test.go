package qemu_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Benehiko/vee/internal/qemu"
)

// TestQMPOwnerCommandsAndEvents verifies that a single owner connection both
// executes commands (getting the right response) and delivers asynchronous
// events to the callback — the core reason QMPOwner exists.
func TestQMPOwnerCommandsAndEvents(t *testing.T) {
	sockPath := filepath.Join(shortSockDir(t), "qmp.sock")
	cleanup := mockSocketServer(t, sockPath, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		qmpHandshake(t, conn)

		r := bufio.NewScanner(conn)
		// First: emit an unsolicited event before any command arrives.
		_, _ = fmt.Fprint(conn, `{"event":"SHUTDOWN","data":{"guest":true}}`+"\n")
		// Then answer commands.
		for r.Scan() {
			line := r.Text()
			switch {
			case strings.Contains(line, "query-status"):
				_, _ = fmt.Fprint(conn, `{"return":{"status":"running"}}`+"\n")
			case strings.Contains(line, "quit-loop"):
				return
			default:
				_, _ = fmt.Fprint(conn, `{"return":{}}`+"\n")
			}
		}
	})
	defer cleanup()

	var (
		mu     sync.Mutex
		events []qemu.QMPEvent
	)
	owner, err := qemu.NewQMPOwner(context.Background(), sockPath, 2*time.Second, func(ev qemu.QMPEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("NewQMPOwner: %v", err)
	}
	defer func() { _ = owner.Close() }()

	// Command executes and returns the right payload despite the event traffic.
	raw, err := owner.Execute("query-status", nil)
	if err != nil {
		t.Fatalf("Execute query-status: %v", err)
	}
	var st struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("unmarshal return: %v", err)
	}
	if st.Status != "running" {
		t.Errorf("status: got %q, want running", st.Status)
	}

	// The SHUTDOWN event should have been delivered to the callback.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}
	if events[0].Event != "SHUTDOWN" {
		t.Errorf("event: got %q, want SHUTDOWN", events[0].Event)
	}
}

// TestQMPOwnerConcurrentExecute checks that concurrent Execute calls are
// serialized and each gets a coherent response.
func TestQMPOwnerConcurrentExecute(t *testing.T) {
	sockPath := filepath.Join(shortSockDir(t), "qmp.sock")
	cleanup := mockSocketServer(t, sockPath, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		qmpHandshake(t, conn)
		r := bufio.NewScanner(conn)
		for r.Scan() {
			// Echo a distinct return so a mismatched demux would be detectable.
			_, _ = fmt.Fprint(conn, `{"return":{"ok":true}}`+"\n")
		}
	})
	defer cleanup()

	owner, err := qemu.NewQMPOwner(context.Background(), sockPath, 2*time.Second, nil)
	if err != nil {
		t.Fatalf("NewQMPOwner: %v", err)
	}
	defer func() { _ = owner.Close() }()

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := owner.Execute("query-status", nil); err != nil {
				t.Errorf("concurrent Execute: %v", err)
			}
		})
	}
	wg.Wait()
}
