package vm

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	hostsFile   = "/etc/hosts"
	hostsMarker = "# vee-managed"
)

// RegisterHostname adds hostname → ip to /etc/hosts (via sudo).
func RegisterHostname(hostname, ip string) error {
	if err := removeHostsEntry(hostname); err != nil {
		return err
	}
	entry := fmt.Sprintf("%s\t%s\t%s.local %s\n", ip, hostname, hostname, hostsMarker)
	cmd := exec.Command("sudo", "tee", "-a", hostsFile)
	cmd.Stdin = strings.NewReader(entry)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write /etc/hosts: %w: %s", err, out)
	}
	return nil
}

// UnregisterHostname removes a vee-managed hostname entry from /etc/hosts (via sudo).
func UnregisterHostname(hostname string) error {
	return removeHostsEntry(hostname)
}

// removeHostsEntry strips lines that contain both the vee marker and hostname.
func removeHostsEntry(hostname string) error {
	f, err := os.Open(hostsFile)
	if err != nil {
		return fmt.Errorf("open %s: %w", hostsFile, err)
	}
	var kept []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, hostsMarker) && strings.Contains(line, hostname) {
			continue
		}
		kept = append(kept, line)
	}
	_ = f.Close()
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", hostsFile, err)
	}

	content := strings.Join(kept, "\n") + "\n"
	cmd := exec.Command("sudo", "tee", hostsFile)
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write %s: %w: %s", hostsFile, err, out)
	}
	return nil
}
