package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var portsCmd = &cobra.Command{
	Use:               "ports <name>",
	Short:             "List bound TCP ports and process names inside a running VM",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeVMNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		_, state, err := loadRunningVM(name)
		if err != nil {
			return err
		}
		if state.QGASocket == "" {
			return fmt.Errorf("VM %q was not started with guest agent support; recreate with a template that enables guest_agent", name)
		}

		client, close, err := openQGAClient(state.QGASocket, 5*time.Second)
		if err != nil {
			return err
		}
		defer close()

		stdout, _, _, err := client.RunCommand("/bin/ss", []string{"-tlnp"})
		if err != nil {
			// Try alternate path
			stdout, _, _, err = client.RunCommand("/usr/sbin/ss", []string{"-tlnp"})
			if err != nil {
				return fmt.Errorf("run ss in guest: %w", err)
			}
		}

		ports := parseSSOutput(stdout)
		if len(ports) == 0 {
			fmt.Println("no listening TCP ports found")
			return nil
		}

		fmt.Printf("%-8s %-20s %s\n", "PORT", "PROCESS", "ADDRESS")
		fmt.Println(strings.Repeat("-", 50))
		for _, p := range ports {
			fmt.Printf("%-8s %-20s %s\n", p.port, p.process, p.addr)
		}
		return nil
	},
}

type portEntry struct {
	port    string
	process string
	addr    string
}

// parseSSOutput parses `ss -tlnp` output into port entries.
// Output format: State Recv-Q Send-Q Local-Address:Port Peer-Address:Port Process
func parseSSOutput(output string) []portEntry {
	var entries []portEntry
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
		entries = append(entries, portEntry{
			port:    port,
			process: process,
			addr:    localAddr,
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
// e.g. users:(("nginx",pid=123,fd=4)) → nginx
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
