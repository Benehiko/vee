package vm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Benehiko/vee/internal/qemu"
)

// GuestPort is one listening TCP socket inside a guest, parsed from
// `ss -tlnp` output.
type GuestPort struct {
	Port    string `json:"port"`
	Process string `json:"process,omitempty"`
	Addr    string `json:"addr"`
}

// QueryGuestPorts runs `ss -tlnp` inside the guest via the QEMU guest agent
// and parses the result. Shared by `vee ports` and the MCP server's vm_ports.
func QueryGuestPorts(ctx context.Context, qgaSocket string, timeout time.Duration) ([]GuestPort, error) {
	client, err := qemu.NewQGAClient(ctx, qgaSocket, timeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()

	stdout, _, _, err := client.RunCommand("/bin/ss", []string{"-tlnp"})
	if err != nil {
		// Try alternate path
		stdout, _, _, err = client.RunCommand("/usr/sbin/ss", []string{"-tlnp"})
		if err != nil {
			return nil, fmt.Errorf("run ss in guest: %w", err)
		}
	}
	return ParseSSOutput(stdout), nil
}

// ParseSSOutput parses `ss -tlnp` output into port entries.
// Output format: State Recv-Q Send-Q Local-Address:Port Peer-Address:Port Process.
func ParseSSOutput(output string) []GuestPort {
	var entries []GuestPort
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "State") || strings.HasPrefix(line, "Netid") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		localAddr := fields[3]
		port := extractPort(localAddr)
		if port == "" {
			continue
		}
		process := ""
		// Process field is the last field when present, looks like users:(("nginx",pid=123,fd=4))
		if len(fields) >= 6 {
			process = extractProcessName(fields[len(fields)-1])
		}
		entries = append(entries, GuestPort{
			Port:    port,
			Process: process,
			Addr:    localAddr,
		})
	}
	return entries
}

// extractPort extracts the port number from an address like 0.0.0.0:80, [::]:443, *:22.
func extractPort(addr string) string {
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		return addr[idx+1:]
	}
	return ""
}

// extractProcessName extracts the first process name from ss process field.
// e.g. users:(("nginx",pid=123,fd=4)) → nginx.
func extractProcessName(field string) string {
	if !strings.HasPrefix(field, "users:") {
		return ""
	}
	// Find the first quoted name inside users:((
	start := strings.Index(field, `"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(field[start+1:], `"`)
	if end < 0 {
		return ""
	}
	return field[start+1 : start+1+end]
}
