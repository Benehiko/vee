package qemu_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Benehiko/vee/internal/qemu"
)

// qmpHandshake writes the QMP greeting and reads+responds to qmp_capabilities.
func qmpHandshake(t *testing.T, conn net.Conn) {
	t.Helper()
	_, err := fmt.Fprintf(conn, `{"QMP":{"version":{},"capabilities":[]}}`+"\n")
	if err != nil {
		t.Errorf("write greeting: %v", err)
		return
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}
	var req map[string]any
	if err := json.Unmarshal(buf[:n], &req); err == nil {
		if req["execute"] != "qmp_capabilities" {
			t.Errorf("expected qmp_capabilities, got %v", req["execute"])
		}
	}
	_, _ = fmt.Fprintf(conn, `{"return":{}}`+"\n")
}

func TestQMPGreetingAndCapabilities(t *testing.T) {
	sockPath := filepath.Join(shortSockDir(t), "qmp.sock")
	cleanup := mockSocketServer(t, sockPath, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		qmpHandshake(t, conn)
		// Keep connection open until client closes.
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
	})
	defer cleanup()

	client, err := qemu.NewQMPClient(context.Background(), sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("NewQMPClient: %v", err)
	}
	defer func() { _ = client.Close() }()
}

func TestQMPCommandSerialization(t *testing.T) {
	sockPath := filepath.Join(shortSockDir(t), "qmp.sock")
	cleanup := mockSocketServer(t, sockPath, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		qmpHandshake(t, conn)

		buf := make([]byte, 512)
		n, _ := conn.Read(buf)
		raw := string(buf[:n])
		if !strings.Contains(raw, `"system_powerdown"`) {
			t.Errorf("expected system_powerdown in request, got: %s", raw)
		}
		_, _ = fmt.Fprintf(conn, `{"return":{}}`+"\n")
	})
	defer cleanup()

	client, err := qemu.NewQMPClient(context.Background(), sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("NewQMPClient: %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.SystemPowerdown(); err != nil {
		t.Fatalf("SystemPowerdown: %v", err)
	}
}

func TestQMPQueryStatus(t *testing.T) {
	sockPath := filepath.Join(shortSockDir(t), "qmp.sock")
	cleanup := mockSocketServer(t, sockPath, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		qmpHandshake(t, conn)

		buf := make([]byte, 512)
		_, _ = conn.Read(buf)
		_, _ = fmt.Fprintf(conn, `{"return":{"status":"running"}}`+"\n")
	})
	defer cleanup()

	client, err := qemu.NewQMPClient(context.Background(), sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("NewQMPClient: %v", err)
	}
	defer func() { _ = client.Close() }()

	status, err := client.QueryStatus()
	if err != nil {
		t.Fatalf("QueryStatus: %v", err)
	}
	if status.Status != "running" {
		t.Errorf("Status: got %q, want %q", status.Status, "running")
	}
}

func TestQMPErrorResponse(t *testing.T) {
	sockPath := filepath.Join(shortSockDir(t), "qmp.sock")
	cleanup := mockSocketServer(t, sockPath, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		qmpHandshake(t, conn)

		buf := make([]byte, 512)
		_, _ = conn.Read(buf)
		_, _ = fmt.Fprintf(conn, `{"error":{"class":"GenericError","desc":"not supported"}}`+"\n")
	})
	defer cleanup()

	client, err := qemu.NewQMPClient(context.Background(), sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("NewQMPClient: %v", err)
	}
	defer func() { _ = client.Close() }()

	err = client.SystemPowerdown()
	if err == nil {
		t.Fatal("expected error from error response, got nil")
	}
	if !strings.Contains(err.Error(), "GenericError") {
		t.Errorf("error should mention GenericError, got: %v", err)
	}
}

func TestQMPDialTimeout(t *testing.T) {
	_, err := qemu.NewQMPClient(context.Background(), "/nonexistent/qmp-timeout.sock", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected dial error, got nil")
	}
}

func TestQMPQueryRaw(t *testing.T) {
	sockPath := filepath.Join(shortSockDir(t), "qmp.sock")
	cleanup := mockSocketServer(t, sockPath, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		qmpHandshake(t, conn)

		buf := make([]byte, 512)
		responses := []string{
			`{"return":{"actual":2147483648}}`,                                    // query-balloon
			`{"return":[{"wall-time-ns":1000000}]}`,                               // query-vcpu-time
			`{"return":[{"stats":{"rd_bytes":512,"wr_bytes":1024},"device":""}]}`, // query-blockstats
			`{"return":[{"rx-bytes":100,"tx-bytes":200,"name":"eth0"}]}`,          // query-net
		}
		for _, resp := range responses {
			_, _ = conn.Read(buf)
			_, _ = fmt.Fprint(conn, resp+"\n")
		}
	})
	defer cleanup()

	client, err := qemu.NewQMPClient(context.Background(), sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("NewQMPClient: %v", err)
	}
	defer func() { _ = client.Close() }()

	counters, err := client.QueryRaw()
	if err != nil {
		t.Fatalf("QueryRaw: %v", err)
	}
	if counters.BalloonActual != 2147483648 {
		t.Errorf("BalloonActual: got %d, want 2147483648", counters.BalloonActual)
	}
	if counters.CPUNs != 1000000 {
		t.Errorf("CPUNs: got %d, want 1000000", counters.CPUNs)
	}
	if counters.DiskRdBytes != 512 {
		t.Errorf("DiskRdBytes: got %d, want 512", counters.DiskRdBytes)
	}
	if counters.DiskWrBytes != 1024 {
		t.Errorf("DiskWrBytes: got %d, want 1024", counters.DiskWrBytes)
	}
	if counters.NetRxBytes != 100 {
		t.Errorf("NetRxBytes: got %d, want 100", counters.NetRxBytes)
	}
	if counters.NetTxBytes != 200 {
		t.Errorf("NetTxBytes: got %d, want 200", counters.NetTxBytes)
	}
}
